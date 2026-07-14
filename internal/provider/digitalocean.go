package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	digitalOceanAPIBaseURL = "https://api.digitalocean.com"
	digitalOceanSourceURL  = "https://api.digitalocean.com/v2/sizes"
	digitalOceanPageSize   = 200
	digitalOceanMaxPages   = 20
	maxDigitalOceanNextURL = 2048
)

type digitalOceanAdapter struct {
	baseURL string
	token   string
	client  *http.Client
	now     func() time.Time
}

type digitalOceanEnvelope struct {
	Sizes *[]digitalOceanSize `json:"sizes"`
	Links struct {
		Pages struct {
			Next string `json:"next"`
		} `json:"pages"`
	} `json:"links"`
}

type digitalOceanSize struct {
	Slug    string               `json:"slug"`
	GPUInfo *digitalOceanGPUInfo `json:"gpu_info"`

	Available *bool        `json:"available"`
	Regions   []string     `json:"regions"`
	VCPUs     *json.Number `json:"vcpus"`
	Memory    *json.Number `json:"memory"`
	Disk      *json.Number `json:"disk"`
	Transfer  *json.Number `json:"transfer"`
	Price     *json.Number `json:"price_hourly"`
}

type digitalOceanGPUInfo struct {
	Count *json.Number      `json:"count"`
	Model string            `json:"model"`
	VRAM  *digitalOceanVRAM `json:"vram"`
}

type digitalOceanVRAM struct {
	Amount *json.Number `json:"amount"`
	Unit   string       `json:"unit"`
}

type digitalOceanPending struct {
	offer       *Offer
	disposition *Disposition
	fallbackID  string
}

type digitalOceanCaptureTransport struct {
	base       http.RoundTripper
	lastHeader http.Header
}

func (t *digitalOceanCaptureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(request)
	if response != nil {
		t.lastHeader = response.Header.Clone()
	} else {
		t.lastHeader = nil
	}
	return response, err
}

func NewDigitalOcean(token string, client *http.Client, now func() time.Time) Adapter {
	return newDigitalOcean(digitalOceanAPIBaseURL, token, client, now)
}

func newDigitalOcean(baseURL, token string, client *http.Client, now func() time.Time) Adapter {
	if client == nil {
		client = http.DefaultClient
	}
	if now == nil {
		now = time.Now
	}
	return &digitalOceanAdapter{
		baseURL: baseURL,
		token:   token,
		client:  client,
		now:     now,
	}
}

func (a *digitalOceanAdapter) Provider() Provider {
	return ProviderDigitalOcean
}

func (a *digitalOceanAdapter) Coverage() Coverage {
	return cloneCoverage(digitalOceanBaselineCoverage())
}

func (a *digitalOceanAdapter) Discover(ctx context.Context) (Snapshot, error) {
	coverage := digitalOceanBaselineCoverage()
	if a.token == "" {
		return a.finish(nil, coverage, newAdapterError(ProviderDigitalOcean, FailureNotConfigured, 0, 0), nil)
	}

	client, capture := digitalOceanCapturingClient(a.client)
	endpoint := a.baseURL + "/v2/sizes?per_page=200&page=1"
	pending := make([]digitalOceanPending, 0)
	currentPage := 1
	for {
		var envelope digitalOceanEnvelope
		requestErr := doJSON(ctx, client, jsonRequest{
			provider:          ProviderDigitalOcean,
			method:            http.MethodGet,
			endpoint:          endpoint,
			bearerToken:       a.token,
			useRateLimitReset: true,
		}, &envelope)
		if requestErr != nil {
			return a.finish(pending, coverage, requestErr, capture.lastHeader)
		}
		if envelope.Sizes == nil {
			return a.finish(pending, coverage, newAdapterError(ProviderDigitalOcean, FailureInvalidResponse, 0, 0), nil)
		}

		for itemIndex, size := range *envelope.Sizes {
			pending = append(pending, normalizeDigitalOceanSize(size, currentPage, itemIndex+1)...)
		}

		next := envelope.Links.Pages.Next
		if next == "" {
			coverage.Complete = true
			return a.finish(pending, coverage, nil, nil)
		}
		if currentPage == digitalOceanMaxPages {
			coverage.Truncated = true
			coverage.Notes = SortReasons(append(coverage.Notes, Reason{
				Code:    ReasonResultsTruncated,
				Field:   "links.pages.next",
				Message: "DigitalOcean size results were truncated at the page limit",
			}))
			return a.finish(pending, coverage, nil, nil)
		}

		nextPage, valid := validateDigitalOceanNextURL(a.baseURL, next, currentPage)
		if !valid {
			return a.finish(pending, coverage, newAdapterError(ProviderDigitalOcean, FailureInvalidResponse, 0, 0), nil)
		}
		endpoint = next
		currentPage = nextPage
	}
}

func digitalOceanCapturingClient(client *http.Client) (*http.Client, *digitalOceanCaptureTransport) {
	cloned := *client
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	capture := &digitalOceanCaptureTransport{base: transport}
	cloned.Transport = capture
	return &cloned, capture
}

func (a *digitalOceanAdapter) finish(pending []digitalOceanPending, coverage Coverage, adapterErr *AdapterError, responseHeader http.Header) (Snapshot, error) {
	observed := a.now().UTC()
	dispositions := make([]Disposition, 0, len(pending))
	seenOfferIDs := make(map[string]struct{}, len(pending))
	for _, item := range pending {
		if item.disposition != nil {
			dispositions = append(dispositions, *item.disposition)
			continue
		}
		offer := *item.offer
		if _, duplicate := seenOfferIDs[offer.ProviderOfferID]; duplicate {
			disposition, err := Rejected(ProviderDigitalOcean, item.fallbackID, digitalOceanInvalid("provider_offer_id", "synthetic offer ID is duplicated"))
			if err != nil {
				return Snapshot{Coverage: cloneCoverage(coverage)}, newAdapterError(ProviderDigitalOcean, FailureInvalidResponse, 0, 0)
			}
			dispositions = append(dispositions, disposition)
			continue
		}
		seenOfferIDs[offer.ProviderOfferID] = struct{}{}
		offer.ObservedAt = observed
		disposition, err := Accepted(offer)
		if err != nil {
			coverage.Complete = false
			coverage.Truncated = false
			return Snapshot{
				Dispositions: SortDispositions(dispositions),
				Coverage:     cloneCoverage(coverage),
			}, newAdapterError(ProviderDigitalOcean, FailureInvalidResponse, 0, 0)
		}
		dispositions = append(dispositions, disposition)
	}

	if adapterErr != nil && adapterErr.HTTPStatus != 0 {
		adapterErr.RetryAfterSeconds = parseRetryAfter(responseHeader, observed, true)
	}
	snapshot := Snapshot{
		Dispositions: SortDispositions(dispositions),
		Coverage:     cloneCoverage(coverage),
	}
	if adapterErr != nil {
		return snapshot, adapterErr
	}
	return snapshot, nil
}

func normalizeDigitalOceanSize(size digitalOceanSize, page, item int) []digitalOceanPending {
	fallbackID := "page-" + strconv.Itoa(page) + "-item-" + strconv.Itoa(item)
	if size.GPUInfo == nil {
		return []digitalOceanPending{digitalOceanRejection(fallbackID, Reason{
			Code:    ReasonNotGPU,
			Field:   "gpu_info",
			Message: "size does not include GPU information",
		})}
	}

	reasons := make([]Reason, 0)
	if size.Available == nil {
		reasons = append(reasons, digitalOceanMissing("available", "size availability is missing"))
	} else if !*size.Available {
		reasons = append(reasons, Reason{Code: ReasonUnavailable, Field: "available", Message: "size is unavailable"})
	}
	if size.Slug == "" {
		reasons = append(reasons, digitalOceanMissing("slug", "size slug is missing"))
	} else if !isSafeNonemptyString(size.Slug) {
		reasons = append(reasons, digitalOceanInvalid("slug", "size slug is invalid"))
	}
	if size.GPUInfo.Model == "" {
		reasons = append(reasons, digitalOceanMissing("gpu_info.model", "GPU model is missing"))
	} else if !isSafeNonemptyString(size.GPUInfo.Model) {
		reasons = append(reasons, digitalOceanInvalid("gpu_info.model", "GPU model is invalid"))
	}

	count, countOK := digitalOceanPositiveInt(size.GPUInfo.Count)
	if size.GPUInfo.Count == nil {
		reasons = append(reasons, digitalOceanMissing("gpu_info.count", "GPU count is missing"))
	} else if !countOK {
		reasons = append(reasons, digitalOceanInvalid("gpu_info.count", "GPU count must be a positive integer"))
	}

	var memoryGiB float64
	if size.GPUInfo.VRAM == nil || size.GPUInfo.VRAM.Amount == nil {
		reasons = append(reasons, digitalOceanMissing("gpu_info.vram.amount", "GPU VRAM amount is missing"))
	} else if amount, ok := digitalOceanPositiveExactInteger(size.GPUInfo.VRAM.Amount); !ok {
		reasons = append(reasons, digitalOceanInvalid("gpu_info.vram.amount", "GPU VRAM amount must be a positive exactly representable integer"))
	} else {
		memoryGiB = amount
	}
	if size.GPUInfo.VRAM == nil || size.GPUInfo.VRAM.Unit == "" {
		reasons = append(reasons, digitalOceanMissing("gpu_info.vram.unit", "GPU VRAM unit is missing"))
	} else if size.GPUInfo.VRAM.Unit != "gib" {
		reasons = append(reasons, digitalOceanInvalid("gpu_info.vram.unit", "GPU VRAM unit must be gib"))
	}

	price := ""
	if size.Price == nil {
		reasons = append(reasons, Reason{Code: ReasonPriceMissing, Field: "price_hourly", Message: "hourly price is missing"})
	} else if canonical, err := CanonicalDecimal(size.Price.String()); err != nil {
		reasons = append(reasons, digitalOceanInvalid("price_hourly", "hourly price must be positive"))
	} else {
		price = canonical
	}

	vcpus := digitalOceanRequiredPositiveInteger(&reasons, "vcpus", "vCPU count", size.VCPUs)
	memoryMB := digitalOceanRequiredPositiveInteger(&reasons, "memory", "memory", size.Memory)
	diskGiB := digitalOceanRequiredPositiveInteger(&reasons, "disk", "disk", size.Disk)
	transferTB := digitalOceanRequiredPositiveFloat(&reasons, "transfer", "transfer", size.Transfer)

	if len(size.Regions) == 0 {
		reasons = append(reasons, Reason{Code: ReasonRegionUnknown, Field: "regions", Message: "size has no regions"})
	} else {
		seen := make(map[string]struct{}, len(size.Regions))
		for _, region := range size.Regions {
			if !isSafeNonemptyString(region) {
				reasons = append(reasons, digitalOceanInvalid("regions", "size region is invalid"))
				continue
			}
			if _, duplicate := seen[region]; duplicate {
				reasons = append(reasons, digitalOceanInvalid("regions", "size regions must be unique"))
			}
			seen[region] = struct{}{}
			if isSafeNonemptyString(size.Slug) && !isSafeNonemptyString(size.Slug+":"+region) {
				reasons = append(reasons, digitalOceanInvalid("regions", "synthetic offer ID is invalid"))
			}
		}
	}

	if len(reasons) != 0 {
		disposition, err := Rejected(ProviderDigitalOcean, fallbackID, reasons...)
		if err != nil {
			return []digitalOceanPending{digitalOceanRejection(fallbackID, Reason{
				Code:    ReasonSchemaMismatch,
				Field:   "sizes",
				Message: "size could not be normalized",
			})}
		}
		return []digitalOceanPending{{disposition: &disposition}}
	}

	pending := make([]digitalOceanPending, 0, len(size.Regions))
	for regionIndex, region := range size.Regions {
		offer := Offer{
			Provider:                 ProviderDigitalOcean,
			ProviderOfferID:          size.Slug + ":" + region,
			ProviderOfferIDSynthetic: true,
			GPUModel:                 size.GPUInfo.Model,
			GPUMemoryGiB:             memoryGiB,
			GPUCount:                 count,
			Region:                   region,
			Availability:             AvailabilityAvailable,
			Resources: Resources{
				VCPUs:              vcpus,
				HostMemoryGiB:      memoryMB / 1024,
				StorageGiB:         diskGiB,
				IncludedTransferTB: transferTB,
			},
			Billing: Billing{
				Mode:                    "on_demand",
				QuotedUSDPerHour:        price,
				RateScope:               RateScopeTotal,
				BillingIncrementSeconds: 1,
				MinimumBillableSeconds:  60,
				MinimumChargeUSD:        "0.01",
			},
			SourceURL: digitalOceanSourceURL,
		}
		pending = append(pending, digitalOceanPending{
			offer:      &offer,
			fallbackID: fallbackID + "-region-" + strconv.Itoa(regionIndex+1),
		})
	}
	return pending
}

func digitalOceanRejection(sourceID string, reason Reason) digitalOceanPending {
	disposition, err := Rejected(ProviderDigitalOcean, sourceID, reason)
	if err != nil {
		panic(err)
	}
	return digitalOceanPending{disposition: &disposition}
}

func digitalOceanMissing(field, message string) Reason {
	return Reason{Code: ReasonMissingRequired, Field: field, Message: message}
}

func digitalOceanInvalid(field, message string) Reason {
	return Reason{Code: ReasonInvalidValue, Field: field, Message: message}
}

func digitalOceanRequiredPositiveFloat(reasons *[]Reason, field, label string, number *json.Number) float64 {
	if number == nil {
		*reasons = append(*reasons, digitalOceanMissing(field, label+" is missing"))
		return 0
	}
	value, ok := digitalOceanPositiveFloat(number)
	if !ok {
		*reasons = append(*reasons, digitalOceanInvalid(field, label+" must be positive"))
		return 0
	}
	return value
}

func digitalOceanRequiredPositiveInteger(reasons *[]Reason, field, label string, number *json.Number) float64 {
	if number == nil {
		*reasons = append(*reasons, digitalOceanMissing(field, label+" is missing"))
		return 0
	}
	value, ok := digitalOceanPositiveExactInteger(number)
	if !ok {
		*reasons = append(*reasons, digitalOceanInvalid(field, label+" must be a positive exactly representable integer"))
		return 0
	}
	return value
}

func digitalOceanPositiveFloat(number *json.Number) (float64, bool) {
	value, err := strconv.ParseFloat(number.String(), 64)
	return value, err == nil && value > 0
}

func digitalOceanPositiveExactInteger(number *json.Number) (float64, bool) {
	value, err := strconv.ParseUint(number.String(), 10, 64)
	if err != nil || value == 0 || value > 1<<53 {
		return 0, false
	}
	return float64(value), true
}

func digitalOceanPositiveInt(number *json.Number) (int, bool) {
	if number == nil {
		return 0, false
	}
	value, err := strconv.ParseInt(number.String(), 10, 32)
	return int(value), err == nil && value > 0
}

func validateDigitalOceanNextURL(baseURL, next string, currentPage int) (int, bool) {
	if len(next) == 0 || len(next) > maxDigitalOceanNextURL || strings.Contains(next, "#") {
		return 0, false
	}
	base, err := url.Parse(baseURL)
	if err != nil || base.Scheme != "https" || base.Hostname() == "" || base.User != nil || base.Opaque != "" || base.Fragment != "" {
		return 0, false
	}
	parsed, err := url.Parse(next)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.Opaque != "" || parsed.Fragment != "" {
		return 0, false
	}
	if !strings.EqualFold(base.Scheme, parsed.Scheme) || !strings.EqualFold(base.Hostname(), parsed.Hostname()) || effectiveHTTPSPort(base) != effectiveHTTPSPort(parsed) {
		return 0, false
	}
	if parsed.Path != "/v2/sizes" || parsed.EscapedPath() != "/v2/sizes" || parsed.RawPath != "" {
		return 0, false
	}

	parts := strings.Split(parsed.RawQuery, "&")
	if len(parts) != 2 {
		return 0, false
	}
	values := make(map[string]string, 2)
	for _, part := range parts {
		pair := strings.Split(part, "=")
		if len(pair) != 2 || pair[0] == "" || pair[1] == "" {
			return 0, false
		}
		key, keyErr := url.QueryUnescape(pair[0])
		value, valueErr := url.QueryUnescape(pair[1])
		if keyErr != nil || valueErr != nil || key != pair[0] || value != pair[1] {
			return 0, false
		}
		if key != "page" && key != "per_page" {
			return 0, false
		}
		if _, duplicate := values[key]; duplicate {
			return 0, false
		}
		values[key] = value
	}
	if values["per_page"] != strconv.Itoa(digitalOceanPageSize) || !isCanonicalPositiveInteger(values["page"]) {
		return 0, false
	}
	page, err := strconv.Atoi(values["page"])
	if err != nil || page <= currentPage || page > digitalOceanMaxPages {
		return 0, false
	}
	return page, true
}

func effectiveHTTPSPort(parsed *url.URL) string {
	if parsed.Port() == "" {
		return "443"
	}
	return parsed.Port()
}

func isCanonicalPositiveInteger(value string) bool {
	if value == "" || value[0] < '1' || value[0] > '9' {
		return false
	}
	for index := 1; index < len(value); index++ {
		if value[index] < '0' || value[index] > '9' {
			return false
		}
	}
	return true
}

func digitalOceanBaselineCoverage() Coverage {
	return Coverage{
		Market: "self_service_gpu_droplet_sizes",
		Limit:  digitalOceanPageSize * digitalOceanMaxPages,
		Notes: []Reason{{
			Code:    ReasonCoverageLimited,
			Field:   "market",
			Message: "per-contract GPU plans are excluded",
		}},
	}
}
