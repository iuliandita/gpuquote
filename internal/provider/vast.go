package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	vastSourceURL   = "https://console.vast.ai/api/v0/bundles/"
	vastRequestBody = `{"limit":100,"type":"on-demand","verified":{"eq":true},"external":{"eq":false},"rentable":{"eq":true},"rented":{"eq":false},"allocated_storage":8,"order":[["dph_total","asc"],["id","asc"]]}`
)

type vastAdapter struct {
	endpoint string
	token    string
	client   *http.Client
	now      func() time.Time
}

type vastEnvelope struct {
	Offers json.RawMessage `json:"offers"`
}

type vastOfferWire struct {
	ID                json.RawMessage `json:"id"`
	GPUName           string          `json:"gpu_name"`
	NumGPUs           json.RawMessage `json:"num_gpus"`
	GPURAM            json.RawMessage `json:"gpu_ram"`
	GPUTotalRAM       json.RawMessage `json:"gpu_total_ram"`
	GPUFrac           json.RawMessage `json:"gpu_frac"`
	Geolocation       string          `json:"geolocation"`
	CPUCoresEffective json.RawMessage `json:"cpu_cores_effective"`
	CPURAM            json.RawMessage `json:"cpu_ram"`
	InetDown          json.RawMessage `json:"inet_down"`
	InetUp            json.RawMessage `json:"inet_up"`
	DPHTotal          json.RawMessage `json:"dph_total"`
	Rentable          *bool           `json:"rentable"`
	Rented            *bool           `json:"rented"`
	External          *bool           `json:"external"`
	Verification      string          `json:"verification"`
	EndDate           json.RawMessage `json:"end_date"`
	Search            json.RawMessage `json:"search"`
}

type vastSearchWire struct {
	GPUCostPerHour json.RawMessage `json:"gpuCostPerHour"`
	DiskHour       json.RawMessage `json:"diskHour"`
	TotalHour      json.RawMessage `json:"totalHour"`
}

func NewVastAI(token string, client *http.Client, now func() time.Time) Adapter {
	return newVastAI(vastSourceURL, token, client, now)
}

func newVastAI(endpoint, token string, client *http.Client, now func() time.Time) Adapter {
	if now == nil {
		now = time.Now
	}
	return &vastAdapter{endpoint: endpoint, token: token, client: client, now: now}
}

func (a *vastAdapter) Provider() Provider { return ProviderVastAI }

func (a *vastAdapter) Coverage() Coverage { return cloneCoverage(vastBaselineCoverage()) }

func (a *vastAdapter) Discover(ctx context.Context) (Snapshot, error) {
	coverage := vastBaselineCoverage()
	if strings.TrimSpace(a.token) == "" {
		return Snapshot{Coverage: cloneCoverage(coverage)}, newAdapterError(ProviderVastAI, FailureNotConfigured, 0, 0)
	}

	observed := a.now().UTC()
	var envelope vastEnvelope
	requestErr := doJSON(ctx, a.client, jsonRequest{
		provider:    ProviderVastAI,
		method:      http.MethodPost,
		endpoint:    a.endpoint,
		bearerToken: a.token,
		body:        []byte(vastRequestBody),
		now:         observed,
	}, &envelope)
	if requestErr != nil {
		if requestErr.HTTPStatus == http.StatusNotFound {
			requestErr = newAdapterError(ProviderVastAI, FailureAuthentication, http.StatusNotFound, requestErr.RetryAfterSeconds)
		}
		return Snapshot{Coverage: cloneCoverage(coverage)}, requestErr
	}
	if vastRawMissing(envelope.Offers) {
		return Snapshot{Coverage: cloneCoverage(coverage)}, newAdapterError(ProviderVastAI, FailureInvalidResponse, 0, 0)
	}
	var items []json.RawMessage
	if !vastDecodeArray(envelope.Offers, &items) || len(items) > coverage.Limit {
		return Snapshot{Coverage: cloneCoverage(coverage)}, newAdapterError(ProviderVastAI, FailureInvalidResponse, 0, 0)
	}

	dispositions := make([]Disposition, 0, len(items))
	seenIDs := make(map[string]struct{}, len(items))
	for index, raw := range items {
		disposition, ok := normalizeVastOffer(raw, index+1, observed, seenIDs)
		if !ok {
			return Snapshot{Coverage: cloneCoverage(coverage)}, newAdapterError(ProviderVastAI, FailureInvalidResponse, 0, 0)
		}
		dispositions = append(dispositions, disposition)
	}

	if len(items) == coverage.Limit {
		coverage.Truncated = true
		coverage.Notes = SortReasons([]Reason{{
			Code:    ReasonResultsTruncated,
			Field:   "offers",
			Message: "search reached the 100-offer response limit",
		}})
	} else {
		coverage.Complete = true
	}
	return Snapshot{Dispositions: SortDispositions(dispositions), Coverage: cloneCoverage(coverage)}, nil
}

func normalizeVastOffer(raw json.RawMessage, itemIndex int, observed time.Time, seenIDs map[string]struct{}) (Disposition, bool) {
	fallbackID := "item-" + strconv.Itoa(itemIndex)
	if vastRawMissing(raw) {
		return vastRejected(fallbackID, vastMissing("offer", "offer is missing"))
	}
	var wire vastOfferWire
	if !vastDecodeObject(raw, &wire) {
		return vastRejected(fallbackID, Reason{Code: ReasonSchemaMismatch, Field: "offer", Message: "offer must be an object"})
	}

	reasons := make([]Reason, 0)
	sourceID, idOK := vastLiteralPositiveInteger(wire.ID, maxPublicStringBytes)
	switch {
	case len(bytes.TrimSpace(wire.ID)) == 0 || vastRawNull(wire.ID):
		reasons = append(reasons, vastMissing("id", "offer ID is missing"))
	case !idOK:
		reasons = append(reasons, vastInvalid("id", "offer ID must be a positive literal integer"))
	case hasVastID(seenIDs, sourceID):
		reasons = append(reasons, Reason{Code: ReasonSchemaMismatch, Field: "id", Message: "offer IDs must be unique"})
		sourceID = fallbackID
	default:
		seenIDs[sourceID] = struct{}{}
	}
	if sourceID == "" {
		sourceID = fallbackID
	}

	if wire.GPUName == "" {
		reasons = append(reasons, vastMissing("gpu_name", "GPU name is missing"))
	} else if !isSafeNonemptyString(wire.GPUName) {
		reasons = append(reasons, vastInvalid("gpu_name", "GPU name is invalid"))
	}
	numGPUs := vastRequiredPositiveInteger(&reasons, "num_gpus", "GPU count", wire.NumGPUs, uint64(^uint(0)>>1))
	gpuRAM := vastRequiredPositiveNumber(&reasons, "gpu_ram", "GPU RAM", wire.GPURAM)
	if canonical, err := vastCanonicalNumber(wire.GPUFrac); err != nil || canonical != "1" {
		if vastRawMissing(wire.GPUFrac) {
			reasons = append(reasons, vastMissing("gpu_frac", "GPU fraction is missing"))
		} else {
			reasons = append(reasons, vastInvalid("gpu_frac", "GPU fraction must equal one"))
		}
	}
	if wire.Geolocation == "" {
		reasons = append(reasons, vastMissing("geolocation", "geolocation is missing"))
	} else if !isSafeNonemptyString(wire.Geolocation) {
		reasons = append(reasons, vastInvalid("geolocation", "geolocation is invalid"))
	}
	vcpus := vastRequiredPositiveNumber(&reasons, "cpu_cores_effective", "effective CPU count", wire.CPUCoresEffective)
	cpuRAM := vastRequiredPositiveNumber(&reasons, "cpu_ram", "CPU RAM", wire.CPURAM)
	inetDown := vastOptionalPositiveNumber(&reasons, "inet_down", "download throughput", wire.InetDown)
	inetUp := vastOptionalPositiveNumber(&reasons, "inet_up", "upload throughput", wire.InetUp)

	if wire.Rentable == nil {
		reasons = append(reasons, vastMissing("rentable", "rentable state is missing"))
	} else if !*wire.Rentable {
		reasons = append(reasons, vastUnavailable("rentable", "offer is not rentable"))
	}
	if wire.Rented == nil {
		reasons = append(reasons, vastMissing("rented", "rented state is missing"))
	} else if *wire.Rented {
		reasons = append(reasons, vastUnavailable("rented", "offer is already rented"))
	}
	if wire.External != nil && *wire.External {
		reasons = append(reasons, vastUnavailable("external", "external offers are excluded"))
	}
	if wire.Verification == "" {
		reasons = append(reasons, vastMissing("verification", "verification state is missing"))
	} else if !isSafeNonemptyString(wire.Verification) {
		reasons = append(reasons, vastInvalid("verification", "verification state is invalid"))
	} else if wire.Verification != "verified" {
		reasons = append(reasons, vastUnavailable("verification", "offer is not verified"))
	}

	if len(bytes.TrimSpace(wire.EndDate)) != 0 && !vastRawNull(wire.EndDate) {
		end, ok := vastPositiveNumber(wire.EndDate)
		if !ok {
			reasons = append(reasons, vastInvalid("end_date", "end date must be a positive Unix timestamp"))
		} else if end <= float64(observed.Unix())+float64(observed.Nanosecond())/1e9 {
			reasons = append(reasons, vastUnavailable("end_date", "offer has expired"))
		}
	}

	price, components := vastPrice(&reasons, wire)
	warnings := []Reason{{
		Code:    ReasonCredentialScopeUnrestricted,
		Field:   "credentials",
		Message: "Vast misc permission includes write operations",
	}}
	if len(bytes.TrimSpace(wire.GPUTotalRAM)) != 0 && !vastRawNull(wire.GPUTotalRAM) {
		total, ok := vastPositiveNumber(wire.GPUTotalRAM)
		if !ok {
			reasons = append(reasons, vastInvalid("gpu_total_ram", "total GPU RAM must be positive"))
		} else if numGPUs != 0 && gpuRAM != 0 && total != gpuRAM*float64(numGPUs) {
			warnings = append(warnings, Reason{Code: ReasonSchemaMismatch, Field: "gpu_total_ram", Message: "total GPU RAM does not match per-GPU RAM and GPU count"})
		}
	}

	if len(reasons) != 0 {
		return vastRejected(sourceID, reasons...)
	}
	offer := Offer{
		Provider:        ProviderVastAI,
		ProviderOfferID: sourceID,
		GPUModel:        wire.GPUName,
		GPUMemoryGiB:    gpuRAM / 1024,
		GPUCount:        int(numGPUs),
		CountryCode:     vastCountryCode(wire.Geolocation),
		LocationLabel:   wire.Geolocation,
		Availability:    AvailabilityAvailable,
		Resources: Resources{
			VCPUs:            vcpus,
			HostMemoryGiB:    cpuRAM / 1024,
			StorageGiB:       8,
			DownloadMBPerSec: inetDown,
			UploadMBPerSec:   inetUp,
		},
		Billing: Billing{
			Mode:                    "on_demand",
			QuotedUSDPerHour:        price,
			RateScope:               RateScopeTotal,
			BillingIncrementSeconds: 1,
			Components:              components,
		},
		ObservedAt: observed,
		SourceURL:  vastSourceURL,
		Warnings:   warnings,
	}
	disposition, err := Accepted(offer)
	return disposition, err == nil
}

func vastPrice(reasons *[]Reason, wire vastOfferWire) (string, []PriceComponent) {
	price := ""
	components := make([]PriceComponent, 0, 2)
	searchValue := bytes.TrimSpace(wire.Search)
	if len(searchValue) != 0 && !bytes.Equal(searchValue, []byte("null")) {
		var search vastSearchWire
		if !vastDecodeObject(wire.Search, &search) {
			*reasons = append(*reasons, Reason{Code: ReasonSchemaMismatch, Field: "search", Message: "search pricing must be an object"})
		} else {
			if !vastRawMissing(search.TotalHour) {
				if value, err := vastCanonicalNumber(search.TotalHour); err == nil {
					price = value
				} else {
					*reasons = append(*reasons, vastInvalid("search.totalHour", "search total price must be positive"))
				}
			}
			for _, component := range []struct {
				name  string
				field string
				raw   json.RawMessage
			}{{"gpu", "search.gpuCostPerHour", search.GPUCostPerHour}, {"storage", "search.diskHour", search.DiskHour}} {
				if vastRawMissing(component.raw) {
					continue
				}
				value, err := vastCanonicalNumber(component.raw)
				if err != nil {
					*reasons = append(*reasons, vastInvalid(component.field, "search price component must be positive"))
					continue
				}
				components = append(components, PriceComponent{Name: component.name, USDPerHour: value})
			}
		}
	}
	if price == "" {
		value, err := vastCanonicalNumber(wire.DPHTotal)
		if err != nil {
			if vastRawMissing(wire.DPHTotal) {
				*reasons = append(*reasons, Reason{Code: ReasonPriceMissing, Field: "dph_total", Message: "total hourly price is missing"})
			} else {
				*reasons = append(*reasons, vastInvalid("dph_total", "total hourly price must be positive"))
			}
		} else {
			price = value
		}
	}
	return price, components
}

func vastRequiredPositiveInteger(reasons *[]Reason, field, label string, raw json.RawMessage, maximum uint64) uint64 {
	value, ok := vastLiteralPositiveInteger(raw, 20)
	if !ok {
		if vastRawMissing(raw) {
			*reasons = append(*reasons, vastMissing(field, label+" is missing"))
		} else {
			*reasons = append(*reasons, vastInvalid(field, label+" must be a positive literal integer"))
		}
		return 0
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil || parsed > maximum {
		*reasons = append(*reasons, vastInvalid(field, label+" is out of range"))
		return 0
	}
	return parsed
}

func vastRequiredPositiveNumber(reasons *[]Reason, field, label string, raw json.RawMessage) float64 {
	if vastRawMissing(raw) {
		*reasons = append(*reasons, vastMissing(field, label+" is missing"))
		return 0
	}
	value, ok := vastPositiveNumber(raw)
	if !ok {
		*reasons = append(*reasons, vastInvalid(field, label+" must be positive"))
		return 0
	}
	return value
}

func vastOptionalPositiveNumber(reasons *[]Reason, field, label string, raw json.RawMessage) float64 {
	if vastRawMissing(raw) {
		return 0
	}
	value, ok := vastPositiveNumber(raw)
	if !ok {
		*reasons = append(*reasons, vastInvalid(field, label+" must be positive when present"))
		return 0
	}
	return value
}

func vastCanonicalNumber(raw json.RawMessage) (string, error) {
	return CanonicalDecimal(string(bytes.TrimSpace(raw)))
}

func vastPositiveNumber(raw json.RawMessage) (float64, bool) {
	canonical, err := vastCanonicalNumber(raw)
	if err != nil {
		return 0, false
	}
	value, err := strconv.ParseFloat(canonical, 64)
	return value, err == nil && value > 0 && !math.IsInf(value, 0) && !math.IsNaN(value)
}

func vastLiteralPositiveInteger(raw json.RawMessage, maximumBytes int) (string, bool) {
	value := bytes.TrimSpace(raw)
	if len(value) == 0 || len(value) > maximumBytes || value[0] < '1' || value[0] > '9' {
		return "", false
	}
	for _, digit := range value[1:] {
		if digit < '0' || digit > '9' {
			return "", false
		}
	}
	return string(value), true
}

func vastDecodeObject(raw json.RawMessage, target any) bool {
	value := bytes.TrimSpace(raw)
	return len(value) >= 2 && value[0] == '{' && value[len(value)-1] == '}' && json.Unmarshal(value, target) == nil
}

func vastDecodeArray(raw json.RawMessage, target any) bool {
	value := bytes.TrimSpace(raw)
	return len(value) >= 2 && value[0] == '[' && value[len(value)-1] == ']' && json.Unmarshal(value, target) == nil
}

func vastRawMissing(raw json.RawMessage) bool {
	return len(bytes.TrimSpace(raw)) == 0 || vastRawNull(raw)
}

func vastRawNull(raw json.RawMessage) bool { return bytes.Equal(bytes.TrimSpace(raw), []byte("null")) }

func hasVastID(seen map[string]struct{}, id string) bool { _, exists := seen[id]; return exists }

func vastCountryCode(label string) string {
	if isTwoUpperASCII(label) {
		return label
	}
	parts := strings.Split(label, ", ")
	if len(parts) < 2 || !isTwoUpperASCII(parts[len(parts)-1]) {
		return ""
	}
	count := 0
	for _, part := range parts {
		if isTwoUpperASCII(part) {
			count++
		}
	}
	if count == 1 {
		return parts[len(parts)-1]
	}
	return ""
}

func isTwoUpperASCII(value string) bool {
	return len(value) == 2 && value[0] >= 'A' && value[0] <= 'Z' && value[1] >= 'A' && value[1] <= 'Z'
}

func vastRejected(sourceID string, reasons ...Reason) (Disposition, bool) {
	disposition, err := Rejected(ProviderVastAI, sourceID, reasons...)
	return disposition, err == nil
}

func vastMissing(field, message string) Reason {
	return Reason{Code: ReasonMissingRequired, Field: field, Message: message}
}

func vastInvalid(field, message string) Reason {
	return Reason{Code: ReasonInvalidValue, Field: field, Message: message}
}

func vastUnavailable(field, message string) Reason {
	return Reason{Code: ReasonUnavailable, Field: field, Message: message}
}

func vastBaselineCoverage() Coverage {
	return Coverage{
		Market:  "on_demand_verified_rentable_unrented",
		Filters: []string{"external=false", "rentable=true", "rented=false", "verified=true"},
		Limit:   100,
	}
}
