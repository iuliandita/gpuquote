package provider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	runPodSourceURL  = "https://api.runpod.io/graphql"
	runPodCountLimit = 16

	runPodCatalogQuery = `query RunPodGPUTypeCatalog {
  gpuTypes {
    id
    displayName
    memoryInGb
    secureCloud
    communityCloud
    maxGpuCountSecureCloud
    maxGpuCountCommunityCloud
  }
}`

	runPodOfferQuery = `query RunPodGPUOffers($gpuCount: Int!) {
  gpuTypes {
    id
    displayName
    memoryInGb
    secureCloud
    communityCloud
    secureOffer: lowestPrice(input: { gpuCount: $gpuCount, secureCloud: true }) {
      gpuTypeId
      gpuName
      uninterruptablePrice
      stockStatus
      availableGpuCounts
      minVcpu
      minMemory
      minDisk
      countryCode
    }
    communityOffer: lowestPrice(input: { gpuCount: $gpuCount, secureCloud: false }) {
      gpuTypeId
      gpuName
      uninterruptablePrice
      stockStatus
      availableGpuCounts
      minVcpu
      minMemory
      minDisk
      countryCode
    }
  }
}`
)

type runPodAdapter struct {
	endpoint string
	token    string
	client   *http.Client
	now      func() time.Time
}

type runPodRequest struct {
	Query     string           `json:"query"`
	Variables *runPodVariables `json:"variables,omitempty"`
}

type runPodVariables struct {
	GPUCount int `json:"gpuCount"`
}

type runPodEnvelope struct {
	Data   json.RawMessage `json:"data"`
	Errors json.RawMessage `json:"errors"`
}

type runPodData struct {
	GPUTypes json.RawMessage `json:"gpuTypes"`
}

type runPodGPUType struct {
	ID                        json.RawMessage `json:"id"`
	DisplayName               json.RawMessage `json:"displayName"`
	MemoryInGB                json.RawMessage `json:"memoryInGb"`
	SecureCloud               json.RawMessage `json:"secureCloud"`
	CommunityCloud            json.RawMessage `json:"communityCloud"`
	MaxGPUCountSecureCloud    json.RawMessage `json:"maxGpuCountSecureCloud"`
	MaxGPUCountCommunityCloud json.RawMessage `json:"maxGpuCountCommunityCloud"`
	SecureOffer               json.RawMessage `json:"secureOffer"`
	CommunityOffer            json.RawMessage `json:"communityOffer"`
}

type runPodLowestPrice struct {
	GPUTypeID            json.RawMessage `json:"gpuTypeId"`
	GPUName              json.RawMessage `json:"gpuName"`
	UninterruptablePrice json.RawMessage `json:"uninterruptablePrice"`
	StockStatus          json.RawMessage `json:"stockStatus"`
	AvailableGPUCounts   json.RawMessage `json:"availableGpuCounts"`
	MinVCPU              json.RawMessage `json:"minVcpu"`
	MinMemory            json.RawMessage `json:"minMemory"`
	MinDisk              json.RawMessage `json:"minDisk"`
	CountryCode          json.RawMessage `json:"countryCode"`
}

type runPodCatalogType struct {
	id           string
	displayName  string
	memoryInGB   int
	secure       bool
	community    bool
	maxSecure    int
	maxCommunity int
	reasons      []Reason
}

func NewRunPod(token string, client *http.Client, now func() time.Time) Adapter {
	return newRunPod(runPodSourceURL, token, client, now)
}

func newRunPod(endpoint, token string, client *http.Client, now func() time.Time) Adapter {
	if now == nil {
		now = time.Now
	}
	return &runPodAdapter{endpoint: endpoint, token: token, client: client, now: now}
}

func (a *runPodAdapter) Provider() Provider { return ProviderRunPod }

func (a *runPodAdapter) Coverage() Coverage { return cloneCoverage(runPodBaselineCoverage()) }

func (a *runPodAdapter) Discover(ctx context.Context) (Snapshot, error) {
	coverage := runPodBaselineCoverage()
	if strings.TrimSpace(a.token) == "" {
		return Snapshot{Coverage: cloneCoverage(coverage)}, newAdapterError(ProviderRunPod, FailureNotConfigured, 0, 0)
	}
	if ctx == nil {
		return Snapshot{Coverage: cloneCoverage(coverage)}, newAdapterError(ProviderRunPod, FailureRequest, 0, 0)
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{Coverage: cloneCoverage(coverage)}, newAdapterError(ProviderRunPod, failureCodeForRequestError(ctx, err, FailureRequest), 0, 0)
	}

	observed := a.now().UTC()
	catalogBody, ok := runPodRequestBody(runPodCatalogQuery, 0)
	if !ok {
		return Snapshot{Coverage: cloneCoverage(coverage)}, newAdapterError(ProviderRunPod, FailureRequest, 0, 0)
	}
	var envelope runPodEnvelope
	requestErr := doJSON(ctx, a.client, jsonRequest{
		provider:    ProviderRunPod,
		method:      http.MethodPost,
		endpoint:    a.endpoint,
		bearerToken: a.token,
		body:        catalogBody,
		now:         observed,
	}, &envelope)
	if requestErr != nil {
		return Snapshot{Coverage: cloneCoverage(coverage)}, requestErr
	}
	catalogItems, valid := runPodEnvelopeItems(envelope)
	if !valid {
		return Snapshot{Coverage: cloneCoverage(coverage)}, newAdapterError(ProviderRunPod, FailureInvalidResponse, 0, 0)
	}
	catalog, largest, valid := runPodCatalog(catalogItems)
	if !valid {
		return Snapshot{Coverage: cloneCoverage(coverage)}, newAdapterError(ProviderRunPod, FailureInvalidResponse, 0, 0)
	}
	if largest > runPodCountLimit {
		coverage.Truncated = true
		coverage.Notes = SortReasons([]Reason{{
			Code:    ReasonResultsTruncated,
			Field:   "gpuTypes.maxGpuCount",
			Message: "RunPod GPU count discovery was truncated at 16",
		}})
	}

	candidates := make(map[string][]Reason)
	completedFollowups := 0
	for count := 1; count <= min(largest, runPodCountLimit); count++ {
		if err := ctx.Err(); err != nil {
			return runPodSnapshot(candidates, coverage, completedFollowups > 0), newAdapterError(ProviderRunPod, failureCodeForRequestError(ctx, err, FailureRequest), 0, 0)
		}
		body, bodyOK := runPodRequestBody(runPodOfferQuery, count)
		if !bodyOK {
			return runPodSnapshot(candidates, coverage, completedFollowups > 0), newAdapterError(ProviderRunPod, FailureRequest, 0, 0)
		}
		envelope = runPodEnvelope{}
		requestErr = doJSON(ctx, a.client, jsonRequest{
			provider:    ProviderRunPod,
			method:      http.MethodPost,
			endpoint:    a.endpoint,
			bearerToken: a.token,
			body:        body,
			now:         observed,
		}, &envelope)
		if requestErr != nil {
			return runPodSnapshot(candidates, coverage, len(candidates) > 0), requestErr
		}
		items, itemsOK := runPodEnvelopeItems(envelope)
		followupCandidates := make(map[string][]Reason)
		if !itemsOK || !runPodCollectFollowup(items, catalog, count, followupCandidates) {
			return runPodSnapshot(candidates, coverage, len(candidates) > 0), newAdapterError(ProviderRunPod, FailureInvalidResponse, 0, 0)
		}
		for sourceID, reasons := range followupCandidates {
			runPodAddCandidate(candidates, sourceID, reasons)
		}
		completedFollowups++
	}

	if !coverage.Truncated {
		coverage.Complete = true
	}
	return runPodSnapshot(candidates, coverage, true), nil
}

func runPodRequestBody(query string, count int) ([]byte, bool) {
	request := runPodRequest{Query: query}
	if count > 0 {
		request.Variables = &runPodVariables{GPUCount: count}
	}
	body, err := json.Marshal(request)
	return body, err == nil
}

func runPodEnvelopeItems(envelope runPodEnvelope) ([]json.RawMessage, bool) {
	if !runPodErrorsEmpty(envelope.Errors) || runPodRawMissing(envelope.Data) {
		return nil, false
	}
	var data runPodData
	if !runPodDecodeObject(envelope.Data, &data) || runPodRawMissing(data.GPUTypes) {
		return nil, false
	}
	var items []json.RawMessage
	if err := json.Unmarshal(data.GPUTypes, &items); err != nil || items == nil {
		return nil, false
	}
	return items, true
}

func runPodErrorsEmpty(raw json.RawMessage) bool {
	if runPodRawMissing(raw) {
		return true
	}
	var items []json.RawMessage
	return json.Unmarshal(raw, &items) == nil && items != nil && len(items) == 0
}

func runPodCatalog(items []json.RawMessage) (map[string]runPodCatalogType, int, bool) {
	catalog := make(map[string]runPodCatalogType, len(items))
	largest := 0
	for index, raw := range items {
		var item runPodGPUType
		if !runPodDecodeObject(raw, &item) {
			return nil, 0, false
		}
		entry := runPodCatalogType{}
		entry.id, _ = runPodSafeString(item.ID)
		if entry.id == "" {
			entry.id = "item-" + strconv.Itoa(index+1)
			entry.reasons = append(entry.reasons, runPodMissing("gpuTypes.id", "GPU type ID is missing or invalid"))
		}
		entry.displayName, _ = runPodSafeString(item.DisplayName)
		if entry.displayName == "" {
			entry.reasons = append(entry.reasons, runPodMissing("gpuTypes.displayName", "GPU display name is missing or invalid"))
		}
		entry.memoryInGB, _ = runPodPositiveInteger(item.MemoryInGB)
		if entry.memoryInGB == 0 {
			entry.reasons = append(entry.reasons, runPodInvalid("gpuTypes.memoryInGb", "GPU memory must be a positive literal integer"))
		}
		var secureOK, communityOK bool
		entry.secure, secureOK = runPodBool(item.SecureCloud)
		entry.community, communityOK = runPodBool(item.CommunityCloud)
		if !secureOK {
			entry.reasons = append(entry.reasons, runPodMissing("gpuTypes.secureCloud", "secure cloud support flag is missing or invalid"))
		}
		if !communityOK {
			entry.reasons = append(entry.reasons, runPodMissing("gpuTypes.communityCloud", "community cloud support flag is missing or invalid"))
		}
		var maxSecureOK, maxCommunityOK bool
		entry.maxSecure, maxSecureOK = runPodNonnegativeInteger(item.MaxGPUCountSecureCloud)
		entry.maxCommunity, maxCommunityOK = runPodNonnegativeInteger(item.MaxGPUCountCommunityCloud)
		if !maxSecureOK {
			return nil, 0, false
		}
		if !maxCommunityOK {
			return nil, 0, false
		}
		if _, duplicate := catalog[entry.id]; duplicate {
			return nil, 0, false
		}
		entry.reasons = SortReasons(entry.reasons)
		catalog[entry.id] = entry
		largest = max(largest, entry.maxSecure, entry.maxCommunity)
	}
	return catalog, largest, true
}

func runPodCollectFollowup(items []json.RawMessage, catalog map[string]runPodCatalogType, requestedCount int, candidates map[string][]Reason) bool {
	seenCatalog := make(map[string]struct{}, len(catalog))
	for index, raw := range items {
		var item runPodGPUType
		if !runPodDecodeObject(raw, &item) {
			return false
		}
		id, idOK := runPodSafeString(item.ID)
		entry, known := catalog[id]
		parentReasons := make([]Reason, 0)
		if !idOK {
			id = runPodFollowupFallbackTypeID(requestedCount, index)
			entry = runPodCatalogType{id: id, maxSecure: requestedCount, maxCommunity: requestedCount}
			parentReasons = append(parentReasons, runPodInvalid("gpuTypes.id", "follow-up GPU type does not match the catalog"))
		} else if !known {
			entry = runPodCatalogType{id: id, maxSecure: requestedCount, maxCommunity: requestedCount}
			parentReasons = append(parentReasons, runPodInvalid("gpuTypes.id", "follow-up GPU type does not match the catalog"))
		} else {
			if _, duplicate := seenCatalog[id]; duplicate {
				return false
			}
			seenCatalog[id] = struct{}{}
			parentReasons = append(parentReasons, entry.reasons...)
		}
		displayName, displayOK := runPodSafeString(item.DisplayName)
		if !displayOK || known && displayName != entry.displayName {
			parentReasons = append(parentReasons, runPodInvalid("gpuTypes.displayName", "follow-up GPU display name does not match the catalog"))
		}
		memory, memoryOK := runPodPositiveInteger(item.MemoryInGB)
		if !memoryOK || known && memory != entry.memoryInGB {
			parentReasons = append(parentReasons, runPodInvalid("gpuTypes.memoryInGb", "follow-up GPU memory does not match the catalog"))
		}
		secure, secureOK := runPodBool(item.SecureCloud)
		community, communityOK := runPodBool(item.CommunityCloud)

		if requestedCount <= entry.maxSecure {
			reasons := append([]Reason(nil), parentReasons...)
			if !secureOK || known && secure != entry.secure {
				reasons = append(reasons, runPodInvalid("gpuTypes.secureCloud", "follow-up secure cloud support does not match the catalog"))
			}
			if !entry.secure {
				reasons = append(reasons, runPodUnavailable("gpuTypes.secureCloud", "GPU type does not support secure cloud"))
			}
			if !runPodCollectOffer(id, entry.displayName, "secure", requestedCount, item.SecureOffer, reasons, candidates) {
				return false
			}
		}
		if requestedCount <= entry.maxCommunity {
			reasons := append([]Reason(nil), parentReasons...)
			if !communityOK || known && community != entry.community {
				reasons = append(reasons, runPodInvalid("gpuTypes.communityCloud", "follow-up community cloud support does not match the catalog"))
			}
			if !entry.community {
				reasons = append(reasons, runPodUnavailable("gpuTypes.communityCloud", "GPU type does not support community cloud"))
			}
			if !runPodCollectOffer(id, entry.displayName, "community", requestedCount, item.CommunityOffer, reasons, candidates) {
				return false
			}
		}
	}
	return len(seenCatalog) == len(catalog)
}

func runPodCollectOffer(typeID, displayName, cloud string, requestedCount int, raw json.RawMessage, parentReasons []Reason, candidates map[string][]Reason) bool {
	reasons := append(parentReasons,
		Reason{Code: ReasonPriceScopeUnknown, Field: "lowestPrice.uninterruptablePrice", Message: "RunPod does not document whether the quoted price is total or per GPU"},
		Reason{Code: ReasonBillingIncrementUnknown, Field: "billing", Message: "RunPod GraphQL does not document the billing increment for this quote"},
		Reason{Code: ReasonMinimumResourcesOnly, Field: "lowestPrice", Message: "RunPod reports minimum host resources rather than an exact allocation"},
		Reason{Code: ReasonRegionUnknown, Field: "lowestPrice.countryCode", Message: "RunPod returns country without a stable region identifier"},
	)
	if runPodRawMissing(raw) {
		reasons = append(reasons, runPodMissing("lowestPrice", "lowest price stock entry is missing"), runPodMissing("lowestPrice.uninterruptablePrice", "quoted price is missing"))
		runPodAddCandidate(candidates, runPodSourceID(typeID, cloud, requestedCount, "unknown"), reasons)
		return true
	}

	var offer runPodLowestPrice
	if !runPodDecodeObject(raw, &offer) {
		return false
	}
	country, countryOK := runPodCountry(offer.CountryCode)
	if !countryOK {
		country = "unknown"
		if runPodRawMissing(offer.CountryCode) {
			reasons = append(reasons, runPodMissing("lowestPrice.countryCode", "country code is missing"))
		} else {
			reasons = append(reasons, runPodInvalid("lowestPrice.countryCode", "country code must be two uppercase ASCII letters"))
		}
	}
	offerType, offerTypeOK := runPodSafeString(offer.GPUTypeID)
	if !offerTypeOK || offerType != typeID {
		reasons = append(reasons, runPodInvalid("lowestPrice.gpuTypeId", "lowest price GPU type does not match the parent type"))
	}
	offerName, nameOK := runPodSafeString(offer.GPUName)
	if !nameOK || displayName != "" && offerName != displayName {
		reasons = append(reasons, runPodInvalid("lowestPrice.gpuName", "lowest price GPU name does not match the parent type"))
	}
	if runPodRawMissing(offer.UninterruptablePrice) {
		reasons = append(reasons, Reason{Code: ReasonPriceMissing, Field: "lowestPrice.uninterruptablePrice", Message: "quoted price is missing"})
	} else if _, err := CanonicalDecimal(string(bytes.TrimSpace(offer.UninterruptablePrice))); err != nil {
		reasons = append(reasons, runPodInvalid("lowestPrice.uninterruptablePrice", "quoted price must be a positive decimal"))
	}
	status, statusOK := runPodSafeString(offer.StockStatus)
	if !statusOK {
		reasons = append(reasons, runPodMissing("lowestPrice.stockStatus", "stock status is missing"))
	} else {
		switch status {
		case "High", "Medium", "Low":
		case "None":
			reasons = append(reasons, runPodUnavailable("lowestPrice.stockStatus", "GPU stock is unavailable"))
		default:
			reasons = append(reasons, runPodInvalid("lowestPrice.stockStatus", "stock status is not recognized"))
		}
	}
	for _, resource := range []struct {
		field string
		raw   json.RawMessage
	}{
		{field: "lowestPrice.minVcpu", raw: offer.MinVCPU},
		{field: "lowestPrice.minMemory", raw: offer.MinMemory},
		{field: "lowestPrice.minDisk", raw: offer.MinDisk},
	} {
		if _, ok := runPodPositiveInteger(resource.raw); !ok {
			reasons = append(reasons, runPodInvalid(resource.field, "minimum resource must be a positive literal integer"))
		}
	}

	counts, countsOK := runPodCounts(offer.AvailableGPUCounts)
	if !countsOK {
		reasons = append(reasons, runPodInvalid("lowestPrice.availableGpuCounts", "available GPU counts must be positive literal integers"))
		counts = nil
	}
	if len(counts) == 0 {
		reasons = append(reasons, runPodMissing("lowestPrice.availableGpuCounts", "requested GPU count is not available"))
		counts = []int{requestedCount}
	} else if !runPodContainsCount(counts, requestedCount) {
		missingReasons := append([]Reason(nil), reasons...)
		missingReasons = append(missingReasons, runPodMissing("lowestPrice.availableGpuCounts", "requested GPU count is not available"))
		runPodAddCandidate(candidates, runPodSourceID(typeID, cloud, requestedCount, country), missingReasons)
	}
	for _, count := range counts {
		countReasons := append([]Reason(nil), reasons...)
		if count > runPodCountLimit {
			countReasons = append(countReasons, runPodInvalid("lowestPrice.availableGpuCounts", "available GPU count exceeds the discovery limit"))
		}
		runPodAddCandidate(candidates, runPodSourceID(typeID, cloud, count, country), countReasons)
	}
	return true
}

func runPodSnapshot(candidates map[string][]Reason, coverage Coverage, warning bool) Snapshot {
	ids := make([]string, 0, len(candidates))
	for id := range candidates {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	dispositions := make([]Disposition, 0, len(ids))
	for _, id := range ids {
		disposition, err := Rejected(ProviderRunPod, id, runPodUniqueReasons(candidates[id])...)
		if err != nil {
			continue
		}
		dispositions = append(dispositions, disposition)
	}
	snapshot := Snapshot{Dispositions: SortDispositions(dispositions), Coverage: cloneCoverage(coverage)}
	if warning {
		snapshot.Warnings = SortReasons([]Reason{{
			Code:    ReasonCredentialScopeUnverified,
			Field:   "credentials",
			Message: "RunPod minimum GraphQL read permission scope is unverified",
		}})
	}
	return snapshot
}

func runPodAddCandidate(candidates map[string][]Reason, sourceID string, reasons []Reason) {
	candidates[sourceID] = append(candidates[sourceID], reasons...)
}

func runPodUniqueReasons(reasons []Reason) []Reason {
	seen := make(map[Reason]struct{}, len(reasons))
	unique := make([]Reason, 0, len(reasons))
	for _, reason := range reasons {
		if _, duplicate := seen[reason]; duplicate {
			continue
		}
		seen[reason] = struct{}{}
		unique = append(unique, reason)
	}
	return SortReasons(unique)
}

func runPodCounts(raw json.RawMessage) ([]int, bool) {
	if runPodRawMissing(raw) {
		return nil, false
	}
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil || values == nil {
		return nil, false
	}
	seen := make(map[int]struct{}, len(values))
	counts := make([]int, 0, len(values))
	for _, value := range values {
		count, ok := runPodPositiveInteger(value)
		if !ok {
			return nil, false
		}
		if _, duplicate := seen[count]; duplicate {
			continue
		}
		seen[count] = struct{}{}
		counts = append(counts, count)
	}
	sort.Ints(counts)
	return counts, true
}

func runPodContainsCount(counts []int, want int) bool {
	index := sort.SearchInts(counts, want)
	return index < len(counts) && counts[index] == want
}

func runPodSourceID(typeID, cloud string, count int, country string) string {
	component := runPodIDComponent(typeID)
	sourceID := component + ":" + cloud + ":" + strconv.Itoa(count) + ":" + country
	if isSafeNonemptyString(sourceID) {
		return sourceID
	}
	digest := sha256.Sum256([]byte(typeID))
	return "type-sha256-" + hex.EncodeToString(digest[:]) + ":" + cloud + ":" + strconv.Itoa(count) + ":" + country
}

func runPodFollowupFallbackTypeID(requestedCount, index int) string {
	// Valid IDs cannot contain NUL, which runPodIDComponent encodes as %00.
	return "\x00followup-" + strconv.Itoa(requestedCount) + "-item-" + strconv.Itoa(index+1)
}

func runPodIDComponent(value string) string {
	const hexadecimal = "0123456789ABCDEF"
	var encoded strings.Builder
	for index := 0; index < len(value); index++ {
		character := value[index]
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '-' || character == '_' || character == '.' {
			encoded.WriteByte(character)
			continue
		}
		encoded.WriteByte('%')
		encoded.WriteByte(hexadecimal[character>>4])
		encoded.WriteByte(hexadecimal[character&15])
	}
	return encoded.String()
}

func runPodDecodeObject(raw json.RawMessage, target any) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) >= 2 && trimmed[0] == '{' && trimmed[len(trimmed)-1] == '}' && json.Unmarshal(trimmed, target) == nil
}

func runPodRawMissing(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null"))
}

func runPodSafeString(raw json.RawMessage) (string, bool) {
	if runPodRawMissing(raw) {
		return "", false
	}
	var value string
	if json.Unmarshal(raw, &value) != nil || !isSafeNonemptyString(value) {
		return "", false
	}
	return value, true
}

func runPodCountry(raw json.RawMessage) (string, bool) {
	value, ok := runPodSafeString(raw)
	return value, ok && value != "" && isCountryCode(value)
}

func runPodBool(raw json.RawMessage) (bool, bool) {
	if runPodRawMissing(raw) {
		return false, false
	}
	var value bool
	return value, json.Unmarshal(raw, &value) == nil
}

func runPodPositiveInteger(raw json.RawMessage) (int, bool) {
	value, ok := runPodNonnegativeInteger(raw)
	return value, ok && value > 0
}

func runPodNonnegativeInteger(raw json.RawMessage) (int, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return 0, false
	}
	for _, character := range trimmed {
		if character < '0' || character > '9' {
			return 0, false
		}
	}
	value, err := strconv.ParseUint(string(trimmed), 10, 31)
	return int(value), err == nil
}

func runPodMissing(field, message string) Reason {
	return Reason{Code: ReasonMissingRequired, Field: field, Message: message}
}

func runPodInvalid(field, message string) Reason {
	return Reason{Code: ReasonInvalidValue, Field: field, Message: message}
}

func runPodUnavailable(field, message string) Reason {
	return Reason{Code: ReasonUnavailable, Field: field, Message: message}
}

func runPodBaselineCoverage() Coverage {
	return Coverage{
		Market:  "secure_and_community_lowest_price",
		Filters: []string{"cloud=community", "cloud=secure"},
		Limit:   runPodCountLimit,
	}
}
