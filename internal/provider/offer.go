package provider

import (
	"fmt"
	"math"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const maxPublicStringBytes = 256

type Provider string

const (
	ProviderRunPod       Provider = "runpod"
	ProviderVastAI       Provider = "vastai"
	ProviderLambda       Provider = "lambda"
	ProviderDigitalOcean Provider = "digitalocean"
)

type Availability string

const (
	AvailabilityAvailable Availability = "available"
	AvailabilityLimited   Availability = "limited"
)

type RateScope string

const (
	RateScopeTotal       RateScope = "total"
	RateScopePerGPU      RateScope = "per_gpu"
	RateScopeUnspecified RateScope = "unspecified"
)

type ReasonCode string

const (
	ReasonNotGPU                      ReasonCode = "not_gpu"
	ReasonUnavailable                 ReasonCode = "unavailable"
	ReasonMissingRequired             ReasonCode = "missing_required"
	ReasonInvalidValue                ReasonCode = "invalid_value"
	ReasonSchemaMismatch              ReasonCode = "schema_mismatch"
	ReasonPriceMissing                ReasonCode = "price_missing"
	ReasonPriceScopeUnknown           ReasonCode = "price_scope_unknown"
	ReasonBillingIncrementUnknown     ReasonCode = "billing_increment_unknown"
	ReasonCredentialScopeUnrestricted ReasonCode = "credential_scope_unrestricted"
	ReasonCredentialScopeUnverified   ReasonCode = "credential_scope_unverified"
	ReasonMinimumResourcesOnly        ReasonCode = "minimum_resources_only"
	ReasonCoverageLimited             ReasonCode = "coverage_limited"
	ReasonResultsTruncated            ReasonCode = "results_truncated"
	ReasonRegionUnknown               ReasonCode = "region_unknown"
)

type Reason struct {
	Code    ReasonCode `json:"code"`
	Field   string     `json:"field,omitempty"`
	Message string     `json:"message"`
}

type Resources struct {
	VCPUs              float64 `json:"vcpus,omitempty"`
	HostMemoryGiB      float64 `json:"host_memory_gib,omitempty"`
	StorageGiB         float64 `json:"storage_gib,omitempty"`
	DownloadMBPerSec   float64 `json:"download_mb_per_second,omitempty"`
	UploadMBPerSec     float64 `json:"upload_mb_per_second,omitempty"`
	IncludedTransferTB float64 `json:"included_transfer_tb,omitempty"`
}

type PriceComponent struct {
	Name       string `json:"name"`
	USDPerHour string `json:"usd_per_hour,omitempty"`
	USDPerGB   string `json:"usd_per_gb,omitempty"`
	USDPerTB   string `json:"usd_per_tb,omitempty"`
}

type Billing struct {
	Mode                    string           `json:"mode"`
	QuotedUSDPerHour        string           `json:"quoted_usd_per_hour"`
	RateScope               RateScope        `json:"rate_scope"`
	BillingIncrementSeconds int64            `json:"billing_increment_seconds,omitempty"`
	MinimumBillableSeconds  int64            `json:"minimum_billable_seconds,omitempty"`
	MinimumChargeUSD        string           `json:"minimum_charge_usd,omitempty"`
	Components              []PriceComponent `json:"components,omitempty"`
}

type Coverage struct {
	Market    string   `json:"market"`
	Filters   []string `json:"filters,omitempty"`
	Limit     int      `json:"limit,omitempty"`
	Complete  bool     `json:"complete"`
	Truncated bool     `json:"truncated"`
	Notes     []Reason `json:"notes,omitempty"`
}

type Offer struct {
	Provider                 Provider     `json:"provider"`
	ProviderOfferID          string       `json:"provider_offer_id"`
	ProviderOfferIDSynthetic bool         `json:"provider_offer_id_synthetic"`
	GPUModel                 string       `json:"gpu_model"`
	GPUMemoryGiB             float64      `json:"gpu_memory_gib,omitempty"`
	GPUCount                 int          `json:"gpu_count"`
	Region                   string       `json:"region,omitempty"`
	CountryCode              string       `json:"country_code,omitempty"`
	LocationLabel            string       `json:"location_label,omitempty"`
	Availability             Availability `json:"availability"`
	Resources                Resources    `json:"resources"`
	Billing                  Billing      `json:"billing"`
	ProviderSourceTime       *time.Time   `json:"provider_source_time,omitempty"`
	ObservedAt               time.Time    `json:"observed_at"`
	SourceURL                string       `json:"source_url"`
	Warnings                 []Reason     `json:"warnings,omitempty"`
}

type DispositionStatus string

const (
	DispositionAccepted DispositionStatus = "accepted"
	DispositionRejected DispositionStatus = "rejected"
)

type Disposition struct {
	Provider Provider          `json:"provider"`
	SourceID string            `json:"source_id"`
	Status   DispositionStatus `json:"status"`
	Offer    *Offer            `json:"offer,omitempty"`
	Reasons  []Reason          `json:"reasons,omitempty"`
}

func (o Offer) Validate() error {
	if err := validateProvider(o.Provider); err != nil {
		return err
	}
	if !isSafeNonemptyString(o.ProviderOfferID) {
		return fmt.Errorf("provider_offer_id must be a safe nonempty string")
	}
	if !isSafeNonemptyString(o.GPUModel) {
		return fmt.Errorf("gpu_model must be a safe nonempty string")
	}
	if err := validatePositiveFloatWhenSet("gpu_memory_gib", o.GPUMemoryGiB); err != nil {
		return err
	}
	if o.GPUCount <= 0 {
		return fmt.Errorf("gpu_count must be greater than zero")
	}
	if o.Availability != AvailabilityAvailable && o.Availability != AvailabilityLimited {
		return fmt.Errorf("availability must be available or limited")
	}
	if !isSafeOptionalString(o.Region) {
		return fmt.Errorf("region must be a safe string when set")
	}
	if !isCountryCode(o.CountryCode) {
		return fmt.Errorf("country_code must be two uppercase ASCII letters when set")
	}
	if !isSafeOptionalString(o.LocationLabel) {
		return fmt.Errorf("location_label must be a safe string when set")
	}
	if err := o.Resources.validate(); err != nil {
		return err
	}
	if err := o.Billing.validate(); err != nil {
		return err
	}
	if o.ObservedAt.IsZero() || o.ObservedAt.Location() != time.UTC {
		return fmt.Errorf("observed_at must be nonzero UTC")
	}
	if o.ProviderSourceTime != nil && (o.ProviderSourceTime.IsZero() || o.ProviderSourceTime.Location() != time.UTC) {
		return fmt.Errorf("provider_source_time must be nonzero UTC when set")
	}
	if !isSafeHTTPSURL(o.SourceURL) {
		return fmt.Errorf("source_url must be an HTTPS URL without credentials")
	}
	for _, warning := range o.Warnings {
		if err := warning.validate(); err != nil {
			return err
		}
	}
	return nil
}

func (d Disposition) Validate() error {
	if err := validateProvider(d.Provider); err != nil {
		return err
	}
	if !isSafeNonemptyString(d.SourceID) {
		return fmt.Errorf("source_id must be a safe nonempty string")
	}

	switch d.Status {
	case DispositionAccepted:
		if d.Offer == nil {
			return fmt.Errorf("accepted disposition must include an offer")
		}
		if len(d.Reasons) != 0 {
			return fmt.Errorf("accepted disposition must not include reasons")
		}
		if d.Provider != d.Offer.Provider || d.SourceID != d.Offer.ProviderOfferID {
			return fmt.Errorf("accepted disposition must match offer provider and source ID")
		}
		return d.Offer.Validate()
	case DispositionRejected:
		if d.Offer != nil {
			return fmt.Errorf("rejected disposition must not include an offer")
		}
		if len(d.Reasons) == 0 {
			return fmt.Errorf("rejected disposition must include at least one reason")
		}
		for _, reason := range d.Reasons {
			if err := reason.validate(); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("disposition status must be accepted or rejected")
	}
}

func (c Coverage) Validate() error {
	if !isSafeNonemptyString(c.Market) {
		return fmt.Errorf("coverage.market must be a safe nonempty string")
	}
	for _, filter := range c.Filters {
		if !isSafeNonemptyString(filter) {
			return fmt.Errorf("coverage.filters must contain safe nonempty strings")
		}
	}
	for index := 1; index < len(c.Filters); index++ {
		if c.Filters[index-1] >= c.Filters[index] {
			return fmt.Errorf("coverage.filters must be sorted and unique")
		}
	}
	if c.Limit < 0 {
		return fmt.Errorf("coverage.limit must be nonnegative")
	}
	for _, note := range c.Notes {
		if err := note.validate(); err != nil {
			return err
		}
	}
	for index := 1; index < len(c.Notes); index++ {
		if compareReasons(c.Notes[index-1], c.Notes[index]) > 0 {
			return fmt.Errorf("coverage.notes must be sorted")
		}
	}
	if c.Complete && c.Truncated {
		return fmt.Errorf("coverage cannot be complete and truncated")
	}
	if c.Truncated && !hasReasonCode(c.Notes, ReasonResultsTruncated) {
		return fmt.Errorf("truncated coverage must include a results_truncated note")
	}
	return nil
}

func Accepted(offer Offer) (Disposition, error) {
	cloned := cloneOffer(offer)
	cloned.Warnings = SortReasons(cloned.Warnings)
	disposition := Disposition{
		Provider: cloned.Provider,
		SourceID: cloned.ProviderOfferID,
		Status:   DispositionAccepted,
		Offer:    &cloned,
	}
	if err := disposition.Validate(); err != nil {
		return Disposition{}, err
	}
	return disposition, nil
}

func Rejected(provider Provider, sourceID string, reasons ...Reason) (Disposition, error) {
	disposition := Disposition{
		Provider: provider,
		SourceID: sourceID,
		Status:   DispositionRejected,
		Reasons:  SortReasons(reasons),
	}
	if err := disposition.Validate(); err != nil {
		return Disposition{}, err
	}
	return disposition, nil
}

func SortDispositions(in []Disposition) []Disposition {
	out := make([]Disposition, len(in))
	for index, disposition := range in {
		out[index] = cloneDisposition(disposition)
		out[index].Reasons = SortReasons(out[index].Reasons)
	}
	sort.SliceStable(out, func(left, right int) bool {
		return compareDispositions(out[left], out[right]) < 0
	})
	return out
}

func SortReasons(in []Reason) []Reason {
	out := append([]Reason(nil), in...)
	sort.SliceStable(out, func(left, right int) bool {
		return compareReasons(out[left], out[right]) < 0
	})
	return out
}

func (r Resources) validate() error {
	fields := []struct {
		name  string
		value float64
	}{
		{name: "resources.vcpus", value: r.VCPUs},
		{name: "resources.host_memory_gib", value: r.HostMemoryGiB},
		{name: "resources.storage_gib", value: r.StorageGiB},
		{name: "resources.download_mb_per_second", value: r.DownloadMBPerSec},
		{name: "resources.upload_mb_per_second", value: r.UploadMBPerSec},
		{name: "resources.included_transfer_tb", value: r.IncludedTransferTB},
	}
	for _, field := range fields {
		if err := validatePositiveFloatWhenSet(field.name, field.value); err != nil {
			return err
		}
	}
	return nil
}

func (b Billing) validate() error {
	if !isSafeNonemptyString(b.Mode) {
		return fmt.Errorf("billing.mode must be a safe nonempty string")
	}
	if !isCanonicalPositiveDecimal(b.QuotedUSDPerHour) {
		return fmt.Errorf("billing.quoted_usd_per_hour must be a canonical positive decimal")
	}
	if b.RateScope != RateScopeTotal && b.RateScope != RateScopePerGPU {
		return fmt.Errorf("billing.rate_scope must be total or per_gpu")
	}
	if b.BillingIncrementSeconds <= 0 {
		return fmt.Errorf("billing.billing_increment_seconds must be greater than zero")
	}
	if b.MinimumBillableSeconds < 0 {
		return fmt.Errorf("billing.minimum_billable_seconds must be nonnegative")
	}
	if !isCanonicalOptionalDecimal(b.MinimumChargeUSD) {
		return fmt.Errorf("billing.minimum_charge_usd must be a canonical positive decimal when set")
	}
	for _, component := range b.Components {
		if !isSafeNonemptyString(component.Name) {
			return fmt.Errorf("billing.components.name must be a safe nonempty string")
		}
		prices := []struct {
			name  string
			value string
		}{
			{name: "billing.components.usd_per_hour", value: component.USDPerHour},
			{name: "billing.components.usd_per_gb", value: component.USDPerGB},
			{name: "billing.components.usd_per_tb", value: component.USDPerTB},
		}
		priceCount := 0
		for _, price := range prices {
			if price.value == "" {
				continue
			}
			priceCount++
			if !isCanonicalPositiveDecimal(price.value) {
				return fmt.Errorf("%s must be a canonical positive decimal when set", price.name)
			}
		}
		if priceCount == 0 {
			return fmt.Errorf("billing.components must include at least one price")
		}
	}
	return nil
}

func (r Reason) validate() error {
	if !isKnownReasonCode(r.Code) {
		return fmt.Errorf("reason code must be recognized")
	}
	if !isSafeOptionalString(r.Field) {
		return fmt.Errorf("reason field must be a safe string when set")
	}
	if !isSafeNonemptyString(r.Message) {
		return fmt.Errorf("reason message must be a safe nonempty string")
	}
	return nil
}

func validateProvider(provider Provider) error {
	switch provider {
	case ProviderRunPod, ProviderVastAI, ProviderLambda, ProviderDigitalOcean:
		return nil
	default:
		return fmt.Errorf("provider must be recognized")
	}
}

func validatePositiveFloatWhenSet(name string, value float64) error {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return fmt.Errorf("%s must be finite and greater than zero when set", name)
	}
	return nil
}

func isCanonicalPositiveDecimal(value string) bool {
	canonical, err := CanonicalDecimal(value)
	return err == nil && canonical == value
}

func isCanonicalOptionalDecimal(value string) bool {
	return value == "" || isCanonicalPositiveDecimal(value)
}

func isSafeNonemptyString(value string) bool {
	if value == "" || len(value) > maxPublicStringBytes || !utf8.ValidString(value) || strings.TrimSpace(value) == "" {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) || unicode.In(character, unicode.Cf, unicode.Zl, unicode.Zp) {
			return false
		}
	}
	return true
}

func isSafeOptionalString(value string) bool {
	return value == "" || isSafeNonemptyString(value)
}

func isCountryCode(value string) bool {
	if value == "" {
		return true
	}
	return len(value) == 2 && value[0] >= 'A' && value[0] <= 'Z' && value[1] >= 'A' && value[1] <= 'Z'
}

func isSafeHTTPSURL(value string) bool {
	if !isSafeNonemptyString(value) {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.Opaque != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	decodedPath, err := url.PathUnescape(parsed.EscapedPath())
	if err != nil || !isSafeOptionalString(decodedPath) {
		return false
	}
	return true
}

func isKnownReasonCode(code ReasonCode) bool {
	switch code {
	case ReasonNotGPU,
		ReasonUnavailable,
		ReasonMissingRequired,
		ReasonInvalidValue,
		ReasonSchemaMismatch,
		ReasonPriceMissing,
		ReasonPriceScopeUnknown,
		ReasonBillingIncrementUnknown,
		ReasonCredentialScopeUnrestricted,
		ReasonCredentialScopeUnverified,
		ReasonMinimumResourcesOnly,
		ReasonCoverageLimited,
		ReasonResultsTruncated,
		ReasonRegionUnknown:
		return true
	default:
		return false
	}
}

func hasReasonCode(reasons []Reason, code ReasonCode) bool {
	for _, reason := range reasons {
		if reason.Code == code {
			return true
		}
	}
	return false
}

func compareReasons(left, right Reason) int {
	if comparison := strings.Compare(string(left.Code), string(right.Code)); comparison != 0 {
		return comparison
	}
	if comparison := strings.Compare(left.Field, right.Field); comparison != 0 {
		return comparison
	}
	return strings.Compare(left.Message, right.Message)
}

func compareDispositions(left, right Disposition) int {
	if comparison := strings.Compare(string(left.Provider), string(right.Provider)); comparison != 0 {
		return comparison
	}
	if comparison := strings.Compare(left.SourceID, right.SourceID); comparison != 0 {
		return comparison
	}
	if comparison := strings.Compare(string(left.Status), string(right.Status)); comparison != 0 {
		return comparison
	}
	common := min(len(left.Reasons), len(right.Reasons))
	for index := 0; index < common; index++ {
		if comparison := compareReasons(left.Reasons[index], right.Reasons[index]); comparison != 0 {
			return comparison
		}
	}
	return len(left.Reasons) - len(right.Reasons)
}

func cloneOffer(offer Offer) Offer {
	cloned := offer
	cloned.Billing.Components = append([]PriceComponent(nil), offer.Billing.Components...)
	cloned.Warnings = append([]Reason(nil), offer.Warnings...)
	if offer.ProviderSourceTime != nil {
		sourceTime := *offer.ProviderSourceTime
		cloned.ProviderSourceTime = &sourceTime
	}
	return cloned
}

func cloneDisposition(disposition Disposition) Disposition {
	cloned := disposition
	cloned.Reasons = append([]Reason(nil), disposition.Reasons...)
	if disposition.Offer != nil {
		offer := cloneOffer(*disposition.Offer)
		cloned.Offer = &offer
	}
	return cloned
}
