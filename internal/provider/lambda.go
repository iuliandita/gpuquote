package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

const lambdaSourceURL = "https://cloud.lambda.ai/api/v1/instance-types"

type lambdaAdapter struct {
	endpoint string
	token    string
	client   *http.Client
	now      func() time.Time
}

type lambdaAPIEnvelope struct {
	Data *map[string]json.RawMessage `json:"data"`
}

type lambdaItemWire struct {
	InstanceType json.RawMessage `json:"instance_type"`
	Regions      json.RawMessage `json:"regions_with_capacity_available"`
}

type lambdaInstanceTypeWire struct {
	Name           string          `json:"name"`
	Description    string          `json:"description"`
	GPUDescription string          `json:"gpu_description"`
	PriceCents     json.RawMessage `json:"price_cents_per_hour"`
	Specs          json.RawMessage `json:"specs"`
}

type lambdaSpecsWire struct {
	VCPUs      json.RawMessage `json:"vcpus"`
	MemoryGiB  json.RawMessage `json:"memory_gib"`
	StorageGiB json.RawMessage `json:"storage_gib"`
	GPUs       json.RawMessage `json:"gpus"`
}

type lambdaRegionWire struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func NewLambda(token string, client *http.Client, now func() time.Time) Adapter {
	return newLambda(lambdaSourceURL, token, client, now)
}

func newLambda(endpoint, token string, client *http.Client, now func() time.Time) Adapter {
	if client == nil {
		client = http.DefaultClient
	}
	if now == nil {
		now = time.Now
	}
	return &lambdaAdapter{
		endpoint: endpoint,
		token:    token,
		client:   client,
		now:      now,
	}
}

func (a *lambdaAdapter) Provider() Provider {
	return ProviderLambda
}

func (a *lambdaAdapter) Coverage() Coverage {
	return cloneCoverage(lambdaBaselineCoverage())
}

func (a *lambdaAdapter) Discover(ctx context.Context) (Snapshot, error) {
	coverage := lambdaBaselineCoverage()
	if a.token == "" {
		return Snapshot{Coverage: cloneCoverage(coverage)}, newAdapterError(ProviderLambda, FailureNotConfigured, 0, 0)
	}

	observed := a.now().UTC()
	var envelope lambdaAPIEnvelope
	requestErr := doJSON(ctx, a.client, jsonRequest{
		provider:    ProviderLambda,
		method:      http.MethodGet,
		endpoint:    a.endpoint,
		bearerToken: a.token,
		now:         observed,
	}, &envelope)
	if requestErr != nil {
		return Snapshot{Coverage: cloneCoverage(coverage)}, requestErr
	}
	if envelope.Data == nil {
		return Snapshot{Coverage: cloneCoverage(coverage)}, newAdapterError(ProviderLambda, FailureInvalidResponse, 0, 0)
	}

	keys := make([]string, 0, len(*envelope.Data))
	for key := range *envelope.Data {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	dispositions := make([]Disposition, 0, len(keys))
	for index, key := range keys {
		normalized, ok := normalizeLambdaItem(key, (*envelope.Data)[key], index+1, observed)
		if !ok {
			return Snapshot{Coverage: cloneCoverage(coverage)}, newAdapterError(ProviderLambda, FailureInvalidResponse, 0, 0)
		}
		dispositions = append(dispositions, normalized...)
	}

	coverage.Complete = true
	return Snapshot{
		Dispositions: SortDispositions(dispositions),
		Coverage:     cloneCoverage(coverage),
	}, nil
}

func normalizeLambdaItem(key string, raw json.RawMessage, sortedIndex int, observed time.Time) ([]Disposition, bool) {
	fallbackID := "item-" + strconv.Itoa(sortedIndex)
	reasons := make([]Reason, 0)
	if key == "" {
		reasons = append(reasons, lambdaMissing("data.key", "instance type key is missing"))
	} else if !lambdaSafeSyntheticIDComponent(key) {
		reasons = append(reasons, lambdaInvalid("data.key", "instance type key is invalid"))
	}
	if lambdaRawMissing(raw) {
		return lambdaRejected(fallbackID, lambdaMissing("data.item", "instance type entry is missing"))
	}

	var item lambdaItemWire
	if !lambdaDecodeObject(raw, &item) {
		return lambdaRejected(fallbackID, Reason{
			Code:    ReasonSchemaMismatch,
			Field:   "data.item",
			Message: "instance type entry must be an object",
		})
	}

	var instance lambdaInstanceTypeWire
	instanceValid := true
	if lambdaRawMissing(item.InstanceType) {
		reasons = append(reasons, lambdaMissing("instance_type", "instance type is missing"))
		instanceValid = false
	} else if !lambdaDecodeObject(item.InstanceType, &instance) {
		reasons = append(reasons, Reason{Code: ReasonSchemaMismatch, Field: "instance_type", Message: "instance type must be an object"})
		instanceValid = false
	}

	if instanceValid {
		lambdaRequireSyntheticIDComponent(&reasons, "instance_type.name", "instance type name", instance.Name)
		lambdaRequireString(&reasons, "instance_type.description", "instance type description", instance.Description)
		lambdaRequireString(&reasons, "instance_type.gpu_description", "GPU description", instance.GPUDescription)
		if isSafeNonemptyString(key) && isSafeNonemptyString(instance.Name) && key != instance.Name {
			reasons = append(reasons, Reason{
				Code:    ReasonSchemaMismatch,
				Field:   "instance_type.name",
				Message: "instance type name does not match its map key",
			})
		}
	}

	priceCents := int64(0)
	if instanceValid {
		if lambdaRawMissing(instance.PriceCents) {
			reasons = append(reasons, Reason{Code: ReasonPriceMissing, Field: "instance_type.price_cents_per_hour", Message: "hourly price is missing"})
		} else if value, ok := lambdaPositiveLiteralInteger(instance.PriceCents, math.MaxInt64); ok {
			priceCents = int64(value)
		} else {
			reasons = append(reasons, lambdaInvalid("instance_type.price_cents_per_hour", "hourly price must be a positive literal integer"))
		}
	}

	var specs lambdaSpecsWire
	specsValid := instanceValid
	if instanceValid && lambdaRawMissing(instance.Specs) {
		reasons = append(reasons, lambdaMissing("instance_type.specs", "instance type specs are missing"))
		specsValid = false
	} else if instanceValid && !lambdaDecodeObject(instance.Specs, &specs) {
		reasons = append(reasons, Reason{Code: ReasonSchemaMismatch, Field: "instance_type.specs", Message: "instance type specs must be an object"})
		specsValid = false
	}

	vcpus := uint64(0)
	memoryGiB := uint64(0)
	storageGiB := uint64(0)
	gpus := uint64(0)
	if specsValid {
		vcpus = lambdaRequiredResourceInteger(&reasons, "instance_type.specs.vcpus", "vCPU count", specs.VCPUs)
		memoryGiB = lambdaRequiredResourceInteger(&reasons, "instance_type.specs.memory_gib", "memory", specs.MemoryGiB)
		storageGiB = lambdaRequiredResourceInteger(&reasons, "instance_type.specs.storage_gib", "storage", specs.StorageGiB)
		gpus = lambdaRequiredGPUInteger(&reasons, specs.GPUs)
	}

	regions := make([]lambdaRegionWire, 0)
	if lambdaRawMissing(item.Regions) {
		reasons = append(reasons, lambdaMissing("regions_with_capacity_available", "available regions are missing"))
	} else {
		var regionValues []json.RawMessage
		if !lambdaDecodeArray(item.Regions, &regionValues) {
			reasons = append(reasons, Reason{Code: ReasonSchemaMismatch, Field: "regions_with_capacity_available", Message: "available regions must be an array"})
		} else if len(regionValues) == 0 {
			reasons = append(reasons, Reason{Code: ReasonUnavailable, Field: "regions_with_capacity_available", Message: "instance type has no available regions"})
		} else {
			seen := make(map[string]string, len(regionValues))
			for _, regionRaw := range regionValues {
				if lambdaRawMissing(regionRaw) {
					reasons = append(reasons, lambdaMissing("regions_with_capacity_available", "available region is missing"))
					continue
				}
				var region lambdaRegionWire
				if !lambdaDecodeObject(regionRaw, &region) {
					reasons = append(reasons, Reason{Code: ReasonSchemaMismatch, Field: "regions_with_capacity_available", Message: "available region must be an object"})
					continue
				}
				lambdaRequireSyntheticIDComponent(&reasons, "regions_with_capacity_available.name", "region name", region.Name)
				lambdaRequireString(&reasons, "regions_with_capacity_available.description", "region description", region.Description)
				if previousDescription, duplicate := seen[region.Name]; duplicate {
					message := "available region names must be unique"
					if previousDescription != region.Description {
						message = "duplicate region names must not have conflicting descriptions"
					}
					reasons = append(reasons, lambdaInvalid("regions_with_capacity_available", message))
				} else {
					seen[region.Name] = region.Description
				}
				if lambdaSafeSyntheticIDComponent(instance.Name) && lambdaSafeSyntheticIDComponent(region.Name) && !isSafeNonemptyString(instance.Name+":"+region.Name) {
					reasons = append(reasons, lambdaInvalid("regions_with_capacity_available", "synthetic offer ID is invalid"))
				}
				regions = append(regions, region)
			}
		}
	}

	if len(reasons) != 0 {
		return lambdaRejected(fallbackID, reasons...)
	}

	price, err := USDFromCents(priceCents)
	if err != nil {
		return nil, false
	}
	dispositions := make([]Disposition, 0, len(regions))
	for _, region := range regions {
		offer := Offer{
			Provider:                 ProviderLambda,
			ProviderOfferID:          instance.Name + ":" + region.Name,
			ProviderOfferIDSynthetic: true,
			GPUModel:                 instance.GPUDescription,
			GPUCount:                 int(gpus),
			Region:                   region.Name,
			LocationLabel:            region.Description,
			Availability:             AvailabilityAvailable,
			Resources: Resources{
				VCPUs:         float64(vcpus),
				HostMemoryGiB: float64(memoryGiB),
				StorageGiB:    float64(storageGiB),
			},
			Billing: Billing{
				Mode:                    "on_demand",
				QuotedUSDPerHour:        price,
				RateScope:               RateScopeTotal,
				BillingIncrementSeconds: 60,
			},
			ObservedAt: observed,
			SourceURL:  lambdaSourceURL,
			Warnings: []Reason{{
				Code:    ReasonCredentialScopeUnrestricted,
				Field:   "credentials",
				Message: "Lambda Cloud API keys have no documented read-only scope",
			}},
		}
		disposition, err := Accepted(offer)
		if err != nil {
			return nil, false
		}
		dispositions = append(dispositions, disposition)
	}
	return dispositions, true
}

func lambdaRejected(sourceID string, reasons ...Reason) ([]Disposition, bool) {
	disposition, err := Rejected(ProviderLambda, sourceID, reasons...)
	if err != nil {
		return nil, false
	}
	return []Disposition{disposition}, true
}

func lambdaRequiredResourceInteger(reasons *[]Reason, field, label string, raw json.RawMessage) uint64 {
	if lambdaRawMissing(raw) {
		*reasons = append(*reasons, lambdaMissing(field, label+" is missing"))
		return 0
	}
	value, ok := lambdaPositiveLiteralInteger(raw, 1<<53)
	if !ok {
		*reasons = append(*reasons, lambdaInvalid(field, label+" must be a positive exactly representable literal integer"))
		return 0
	}
	return value
}

func lambdaRequiredGPUInteger(reasons *[]Reason, raw json.RawMessage) uint64 {
	const maxInt = uint64(^uint(0) >> 1)
	field := "instance_type.specs.gpus"
	if lambdaRawMissing(raw) {
		*reasons = append(*reasons, lambdaMissing(field, "GPU count is missing"))
		return 0
	}
	value, ok := lambdaPositiveLiteralInteger(raw, maxInt)
	if !ok {
		*reasons = append(*reasons, lambdaInvalid(field, "GPU count must be a positive literal integer that fits int"))
		return 0
	}
	return value
}

func lambdaPositiveLiteralInteger(raw json.RawMessage, maximum uint64) (uint64, bool) {
	value := bytes.TrimSpace(raw)
	if len(value) == 0 || value[0] < '1' || value[0] > '9' {
		return 0, false
	}
	for index := 1; index < len(value); index++ {
		if value[index] < '0' || value[index] > '9' {
			return 0, false
		}
	}
	parsed, err := strconv.ParseUint(string(value), 10, 64)
	return parsed, err == nil && parsed <= maximum
}

func lambdaDecodeObject(raw json.RawMessage, target any) bool {
	value := bytes.TrimSpace(raw)
	return len(value) >= 2 && value[0] == '{' && value[len(value)-1] == '}' && json.Unmarshal(value, target) == nil
}

func lambdaDecodeArray(raw json.RawMessage, target any) bool {
	value := bytes.TrimSpace(raw)
	return len(value) >= 2 && value[0] == '[' && value[len(value)-1] == ']' && json.Unmarshal(value, target) == nil
}

func lambdaRawMissing(raw json.RawMessage) bool {
	value := bytes.TrimSpace(raw)
	return len(value) == 0 || bytes.Equal(value, []byte("null"))
}

func lambdaRequireString(reasons *[]Reason, field, label, value string) {
	if value == "" {
		*reasons = append(*reasons, lambdaMissing(field, label+" is missing"))
	} else if !isSafeNonemptyString(value) {
		*reasons = append(*reasons, lambdaInvalid(field, label+" is invalid"))
	}
}

func lambdaRequireSyntheticIDComponent(reasons *[]Reason, field, label, value string) {
	if value == "" {
		*reasons = append(*reasons, lambdaMissing(field, label+" is missing"))
	} else if !lambdaSafeSyntheticIDComponent(value) {
		*reasons = append(*reasons, lambdaInvalid(field, label+" is invalid"))
	}
}

func lambdaSafeSyntheticIDComponent(value string) bool {
	return isSafeNonemptyString(value) && !strings.ContainsRune(value, ':')
}

func lambdaMissing(field, message string) Reason {
	return Reason{Code: ReasonMissingRequired, Field: field, Message: message}
}

func lambdaInvalid(field, message string) Reason {
	return Reason{Code: ReasonInvalidValue, Field: field, Message: message}
}

func lambdaBaselineCoverage() Coverage {
	return Coverage{Market: "instance_types_available_regions"}
}
