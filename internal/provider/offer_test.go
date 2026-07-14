package provider

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

func validOffer() Offer {
	sourceTime := time.Date(2026, time.July, 14, 10, 59, 0, 0, time.UTC)
	return Offer{
		Provider:                 ProviderDigitalOcean,
		ProviderOfferID:          "synthetic-do-1",
		ProviderOfferIDSynthetic: true,
		GPUModel:                 "Example GPU 24GB",
		GPUMemoryGiB:             24,
		GPUCount:                 1,
		Region:                   "test-region-1",
		CountryCode:              "DE",
		LocationLabel:            "Example location",
		Availability:             AvailabilityAvailable,
		Resources: Resources{
			VCPUs:              8,
			HostMemoryGiB:      32,
			StorageGiB:         100,
			DownloadMBPerSec:   250,
			UploadMBPerSec:     125,
			IncludedTransferTB: 2,
		},
		Billing: Billing{
			Mode:                    "on_demand",
			QuotedUSDPerHour:        "0.35",
			RateScope:               RateScopeTotal,
			BillingIncrementSeconds: 1,
			MinimumBillableSeconds:  60,
			MinimumChargeUSD:        "0.01",
			Components: []PriceComponent{
				{Name: "compute", USDPerHour: "0.35"},
			},
		},
		ProviderSourceTime: &sourceTime,
		ObservedAt:         time.Date(2026, time.July, 14, 11, 0, 0, 0, time.UTC),
		SourceURL:          "https://catalog.example.test/offers/synthetic-do-1",
		Warnings: []Reason{
			{Code: ReasonCoverageLimited, Field: "region", Message: "synthetic regional coverage"},
		},
	}
}

func TestOfferValidateAcceptsCompleteTotalRateOffer(t *testing.T) {
	if err := validOffer().Validate(); err != nil {
		t.Fatalf("Offer.Validate() error = %v, want nil", err)
	}
}

func TestOfferValidateRejectsUnknownRateScope(t *testing.T) {
	offer := validOffer()
	offer.Provider = ProviderRunPod
	offer.ProviderOfferID = "synthetic-runpod-1"
	offer.Billing.RateScope = RateScopeUnspecified

	if err := offer.Validate(); err == nil || err.Error() != "billing.rate_scope must be total or per_gpu" {
		t.Fatalf("Offer.Validate() error = %v, want rate scope error", err)
	}

	disposition, err := Rejected(
		ProviderRunPod,
		"synthetic-runpod-1",
		Reason{Code: ReasonPriceScopeUnknown, Field: "price", Message: "price scope unavailable"},
	)
	if err != nil {
		t.Fatalf("Rejected() error = %v, want nil", err)
	}
	if err := disposition.Validate(); err != nil {
		t.Fatalf("Disposition.Validate() error = %v, want nil", err)
	}
}

func TestOfferValidateReturnsStableErrors(t *testing.T) {
	nonUTC := time.Date(2026, time.July, 14, 11, 0, 0, 0, time.FixedZone("offset", 3600))
	zeroUTC := time.Time{}.UTC()

	tests := []struct {
		name    string
		modify  func(*Offer)
		wantErr string
	}{
		{name: "empty provider", modify: func(o *Offer) { o.Provider = "" }, wantErr: "provider must be recognized"},
		{name: "unknown provider", modify: func(o *Offer) { o.Provider = "private-provider" }, wantErr: "provider must be recognized"},
		{name: "empty offer ID", modify: func(o *Offer) { o.ProviderOfferID = "" }, wantErr: "provider_offer_id must be a safe nonempty string"},
		{name: "offer ID control", modify: func(o *Offer) { o.ProviderOfferID = "bad\nvalue" }, wantErr: "provider_offer_id must be a safe nonempty string"},
		{name: "offer ID invalid UTF-8", modify: func(o *Offer) { o.ProviderOfferID = string([]byte{0xff}) }, wantErr: "provider_offer_id must be a safe nonempty string"},
		{name: "offer ID too long", modify: func(o *Offer) { o.ProviderOfferID = strings.Repeat("a", 257) }, wantErr: "provider_offer_id must be a safe nonempty string"},
		{name: "empty GPU model", modify: func(o *Offer) { o.GPUModel = "" }, wantErr: "gpu_model must be a safe nonempty string"},
		{name: "GPU model Unicode control", modify: func(o *Offer) { o.GPUModel = "GPU\u202e" }, wantErr: "gpu_model must be a safe nonempty string"},
		{name: "non-positive GPU count", modify: func(o *Offer) { o.GPUCount = 0 }, wantErr: "gpu_count must be greater than zero"},
		{name: "unknown availability", modify: func(o *Offer) { o.Availability = "unknown" }, wantErr: "availability must be available or limited"},
		{name: "zero observed time", modify: func(o *Offer) { o.ObservedAt = time.Time{} }, wantErr: "observed_at must be nonzero UTC"},
		{name: "non-UTC observed time", modify: func(o *Offer) { o.ObservedAt = nonUTC }, wantErr: "observed_at must be nonzero UTC"},
		{name: "zero source time", modify: func(o *Offer) { o.ProviderSourceTime = &zeroUTC }, wantErr: "provider_source_time must be nonzero UTC when set"},
		{name: "non-UTC source time", modify: func(o *Offer) { o.ProviderSourceTime = &nonUTC }, wantErr: "provider_source_time must be nonzero UTC when set"},
		{name: "missing billing mode", modify: func(o *Offer) { o.Billing.Mode = "" }, wantErr: "billing.mode must be a safe nonempty string"},
		{name: "invalid quoted price", modify: func(o *Offer) { o.Billing.QuotedUSDPerHour = "0" }, wantErr: "billing.quoted_usd_per_hour must be a canonical positive decimal"},
		{name: "noncanonical quoted price", modify: func(o *Offer) { o.Billing.QuotedUSDPerHour = "0.350" }, wantErr: "billing.quoted_usd_per_hour must be a canonical positive decimal"},
		{name: "missing billing increment", modify: func(o *Offer) { o.Billing.BillingIncrementSeconds = 0 }, wantErr: "billing.billing_increment_seconds must be greater than zero"},
		{name: "negative billing increment", modify: func(o *Offer) { o.Billing.BillingIncrementSeconds = -1 }, wantErr: "billing.billing_increment_seconds must be greater than zero"},
		{name: "negative billing minimum", modify: func(o *Offer) { o.Billing.MinimumBillableSeconds = -1 }, wantErr: "billing.minimum_billable_seconds must be nonnegative"},
		{name: "invalid minimum charge", modify: func(o *Offer) { o.Billing.MinimumChargeUSD = "free" }, wantErr: "billing.minimum_charge_usd must be a canonical positive decimal when set"},
		{name: "empty component name", modify: func(o *Offer) { o.Billing.Components[0].Name = "" }, wantErr: "billing.components.name must be a safe nonempty string"},
		{name: "component without rate", modify: func(o *Offer) { o.Billing.Components[0] = PriceComponent{Name: "compute"} }, wantErr: "billing.components must include at least one price"},
		{name: "invalid component price", modify: func(o *Offer) { o.Billing.Components[0].USDPerHour = "NaN" }, wantErr: "billing.components.usd_per_hour must be a canonical positive decimal when set"},
		{name: "HTTP provenance", modify: func(o *Offer) { o.SourceURL = "http://catalog.example.test/offer" }, wantErr: "source_url must be an HTTPS URL without credentials"},
		{name: "credential provenance", modify: func(o *Offer) { o.SourceURL = "https://user:secret@catalog.example.test/offer" }, wantErr: "source_url must be an HTTPS URL without credentials"},
		{name: "provenance without hostname", modify: func(o *Offer) { o.SourceURL = "https://:443/catalog" }, wantErr: "source_url must be an HTTPS URL without credentials"},
		{name: "malformed credential query", modify: func(o *Offer) { o.SourceURL = "https://catalog.example.test/offer?token=secret;ignored" }, wantErr: "source_url must be an HTTPS URL without credentials"},
		{name: "signed query provenance", modify: func(o *Offer) { o.SourceURL = "https://catalog.example.test/offer?X-Amz-Signature=secret" }, wantErr: "source_url must be an HTTPS URL without credentials"},
		{name: "credential fragment provenance", modify: func(o *Offer) { o.SourceURL = "https://catalog.example.test/offer#access_token=secret" }, wantErr: "source_url must be an HTTPS URL without credentials"},
		{name: "unsafe region", modify: func(o *Offer) { o.Region = "bad\x00region" }, wantErr: "region must be a safe string when set"},
		{name: "invalid country", modify: func(o *Offer) { o.CountryCode = "de" }, wantErr: "country_code must be two uppercase ASCII letters when set"},
		{name: "unsafe location", modify: func(o *Offer) { o.LocationLabel = strings.Repeat("x", 257) }, wantErr: "location_label must be a safe string when set"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			offer := validOffer()
			test.modify(&offer)
			err := offer.Validate()
			if err == nil {
				t.Fatal("Offer.Validate() error = nil, want non-nil")
			}
			if err.Error() != test.wantErr {
				t.Errorf("Offer.Validate() error = %q, want %q", err, test.wantErr)
			}
		})
	}
}

func TestOfferValidateRejectsInvalidOptionalFloats(t *testing.T) {
	tests := []struct {
		name    string
		modify  func(*Offer)
		wantErr string
	}{
		{name: "GPU memory NaN", modify: func(o *Offer) { o.GPUMemoryGiB = math.NaN() }, wantErr: "gpu_memory_gib must be finite and greater than zero when set"},
		{name: "GPU memory infinity", modify: func(o *Offer) { o.GPUMemoryGiB = math.Inf(1) }, wantErr: "gpu_memory_gib must be finite and greater than zero when set"},
		{name: "GPU memory negative", modify: func(o *Offer) { o.GPUMemoryGiB = -1 }, wantErr: "gpu_memory_gib must be finite and greater than zero when set"},
		{name: "VCPUs negative", modify: func(o *Offer) { o.Resources.VCPUs = -1 }, wantErr: "resources.vcpus must be finite and greater than zero when set"},
		{name: "memory NaN", modify: func(o *Offer) { o.Resources.HostMemoryGiB = math.NaN() }, wantErr: "resources.host_memory_gib must be finite and greater than zero when set"},
		{name: "storage infinity", modify: func(o *Offer) { o.Resources.StorageGiB = math.Inf(1) }, wantErr: "resources.storage_gib must be finite and greater than zero when set"},
		{name: "download negative", modify: func(o *Offer) { o.Resources.DownloadMBPerSec = -1 }, wantErr: "resources.download_mb_per_second must be finite and greater than zero when set"},
		{name: "upload NaN", modify: func(o *Offer) { o.Resources.UploadMBPerSec = math.NaN() }, wantErr: "resources.upload_mb_per_second must be finite and greater than zero when set"},
		{name: "transfer infinity", modify: func(o *Offer) { o.Resources.IncludedTransferTB = math.Inf(1) }, wantErr: "resources.included_transfer_tb must be finite and greater than zero when set"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			offer := validOffer()
			test.modify(&offer)
			err := offer.Validate()
			if err == nil || err.Error() != test.wantErr {
				t.Errorf("Offer.Validate() error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestReasonValidation(t *testing.T) {
	tests := []struct {
		name    string
		reason  Reason
		wantErr string
	}{
		{name: "unknown code", reason: Reason{Code: "unknown", Message: "public message"}, wantErr: "reason code must be recognized"},
		{name: "empty message", reason: Reason{Code: ReasonInvalidValue}, wantErr: "reason message must be a safe nonempty string"},
		{name: "unsafe field", reason: Reason{Code: ReasonInvalidValue, Field: "bad\nfield", Message: "public message"}, wantErr: "reason field must be a safe string when set"},
		{name: "unsafe message", reason: Reason{Code: ReasonInvalidValue, Message: strings.Repeat("m", 257)}, wantErr: "reason message must be a safe nonempty string"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Rejected(ProviderLambda, "synthetic-lambda-1", test.reason)
			if err == nil || err.Error() != test.wantErr {
				t.Errorf("Rejected() error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestDispositionValidation(t *testing.T) {
	acceptedOffer := validOffer()

	tests := []struct {
		name        string
		disposition Disposition
		wantErr     string
	}{
		{name: "unknown status", disposition: Disposition{Provider: ProviderLambda, SourceID: "source-1", Status: "unknown"}, wantErr: "disposition status must be accepted or rejected"},
		{name: "unknown provider", disposition: Disposition{Provider: "unknown", SourceID: "source-1", Status: DispositionRejected, Reasons: []Reason{{Code: ReasonUnavailable, Message: "unavailable"}}}, wantErr: "provider must be recognized"},
		{name: "unsafe source ID", disposition: Disposition{Provider: ProviderLambda, SourceID: "bad\nsource", Status: DispositionRejected, Reasons: []Reason{{Code: ReasonUnavailable, Message: "unavailable"}}}, wantErr: "source_id must be a safe nonempty string"},
		{name: "accepted without offer", disposition: Disposition{Provider: ProviderDigitalOcean, SourceID: acceptedOffer.ProviderOfferID, Status: DispositionAccepted}, wantErr: "accepted disposition must include an offer"},
		{name: "accepted with reasons", disposition: Disposition{Provider: acceptedOffer.Provider, SourceID: acceptedOffer.ProviderOfferID, Status: DispositionAccepted, Offer: &acceptedOffer, Reasons: []Reason{{Code: ReasonUnavailable, Message: "unavailable"}}}, wantErr: "accepted disposition must not include reasons"},
		{name: "accepted provider mismatch", disposition: Disposition{Provider: ProviderLambda, SourceID: acceptedOffer.ProviderOfferID, Status: DispositionAccepted, Offer: &acceptedOffer}, wantErr: "accepted disposition must match offer provider and source ID"},
		{name: "accepted source mismatch", disposition: Disposition{Provider: acceptedOffer.Provider, SourceID: "different", Status: DispositionAccepted, Offer: &acceptedOffer}, wantErr: "accepted disposition must match offer provider and source ID"},
		{name: "rejected with offer", disposition: Disposition{Provider: acceptedOffer.Provider, SourceID: acceptedOffer.ProviderOfferID, Status: DispositionRejected, Offer: &acceptedOffer, Reasons: []Reason{{Code: ReasonUnavailable, Message: "unavailable"}}}, wantErr: "rejected disposition must not include an offer"},
		{name: "rejected without reasons", disposition: Disposition{Provider: ProviderLambda, SourceID: "source-1", Status: DispositionRejected}, wantErr: "rejected disposition must include at least one reason"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.disposition.Validate()
			if err == nil || err.Error() != test.wantErr {
				t.Errorf("Disposition.Validate() error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestAcceptedAndRejectedDoNotAliasCallerData(t *testing.T) {
	offer := validOffer()
	offerSnapshot := deepCopyOfferForTest(offer)
	accepted, err := Accepted(offer)
	if err != nil {
		t.Fatalf("Accepted() error = %v, want nil", err)
	}
	if accepted.Offer == nil {
		t.Fatal("Accepted() offer = nil")
	}
	if accepted.Offer.ProviderSourceTime == nil {
		t.Fatal("Accepted() provider source time = nil")
	}
	accepted.Offer.Billing.Components[0].Name = "mutated component"
	accepted.Offer.Warnings[0].Message = "mutated warning"
	mutatedSourceTime := accepted.Offer.ProviderSourceTime.Add(time.Hour)
	*accepted.Offer.ProviderSourceTime = mutatedSourceTime
	if !reflect.DeepEqual(offer, offerSnapshot) {
		t.Error("Accepted() result aliases caller offer data")
	}
	reasons := []Reason{
		{Code: ReasonUnavailable, Message: "currently unavailable"},
		{Code: ReasonInvalidValue, Field: "price", Message: "invalid price"},
	}
	reasonsSnapshot := append([]Reason(nil), reasons...)
	rejected, err := Rejected(ProviderVastAI, "synthetic-vast-1", reasons...)
	if err != nil {
		t.Fatalf("Rejected() error = %v, want nil", err)
	}
	if rejected.Reasons[0].Code != ReasonInvalidValue {
		t.Error("Rejected() reasons are not deterministically sorted")
	}
	rejected.Reasons[0].Message = "mutated reason"
	if !reflect.DeepEqual(reasons, reasonsSnapshot) {
		t.Error("Rejected() result aliases caller reasons")
	}
}

func TestSortReasonsReturnsNewDeterministicSlice(t *testing.T) {
	in := []Reason{
		{Code: ReasonUnavailable, Message: "z"},
		{Code: ReasonInvalidValue, Field: "z", Message: "a"},
		{Code: ReasonInvalidValue, Field: "a", Message: "z"},
	}
	original := append([]Reason(nil), in...)

	got := SortReasons(in)
	want := []Reason{
		{Code: ReasonInvalidValue, Field: "a", Message: "z"},
		{Code: ReasonInvalidValue, Field: "z", Message: "a"},
		{Code: ReasonUnavailable, Message: "z"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SortReasons() = %#v, want %#v", got, want)
	}
	if !reflect.DeepEqual(in, original) {
		t.Error("SortReasons() mutated input")
	}
	got[0].Message = "mutated"
	if in[2].Message == "mutated" {
		t.Error("SortReasons() result aliases input")
	}
}

func TestSortDispositionsReturnsNewDeterministicSlice(t *testing.T) {
	acceptedDigitalOcean, err := Accepted(validOffer())
	if err != nil {
		t.Fatalf("Accepted() error = %v", err)
	}
	runPodOffer := validOffer()
	runPodOffer.Provider = ProviderRunPod
	runPodOffer.ProviderOfferID = "shared-status"
	acceptedRunPod, err := Accepted(runPodOffer)
	if err != nil {
		t.Fatalf("Accepted() error = %v", err)
	}

	commonReason := Reason{Code: ReasonInvalidValue, Field: "price", Message: "invalid"}
	rejectedByPrice := Disposition{
		Provider: ProviderRunPod,
		SourceID: "shared-reason",
		Status:   DispositionRejected,
		Reasons: []Reason{
			{Code: ReasonPriceMissing, Field: "price", Message: "price missing"},
			commonReason,
		},
	}
	rejectedByAvailability := Disposition{
		Provider: ProviderRunPod,
		SourceID: "shared-reason",
		Status:   DispositionRejected,
		Reasons: []Reason{
			{Code: ReasonUnavailable, Message: "unavailable"},
			commonReason,
		},
	}
	rejectedSameStatus := Disposition{Provider: ProviderRunPod, SourceID: "shared-status", Status: DispositionRejected, Reasons: []Reason{{Code: ReasonUnavailable, Message: "unavailable"}}}
	rejectedSourceA := Disposition{Provider: ProviderRunPod, SourceID: "source-a", Status: DispositionRejected, Reasons: []Reason{{Code: ReasonUnavailable, Message: "unavailable"}}}
	rejectedSourceB := Disposition{Provider: ProviderRunPod, SourceID: "source-b", Status: DispositionRejected, Reasons: []Reason{{Code: ReasonUnavailable, Message: "unavailable"}}}

	in := []Disposition{
		rejectedSourceB,
		rejectedByAvailability,
		rejectedSameStatus,
		acceptedDigitalOcean,
		rejectedSourceA,
		acceptedRunPod,
		rejectedByPrice,
	}
	for index, disposition := range in {
		if err := disposition.Validate(); err != nil {
			t.Fatalf("fixture %d validation error = %v", index, err)
		}
	}
	original := deepCopyDispositionsForTest(in)
	got := SortDispositions(in)

	want := []struct {
		provider Provider
		sourceID string
		status   DispositionStatus
		reasons  []ReasonCode
	}{
		{provider: ProviderDigitalOcean, sourceID: "synthetic-do-1", status: DispositionAccepted},
		{provider: ProviderRunPod, sourceID: "shared-reason", status: DispositionRejected, reasons: []ReasonCode{ReasonInvalidValue, ReasonPriceMissing}},
		{provider: ProviderRunPod, sourceID: "shared-reason", status: DispositionRejected, reasons: []ReasonCode{ReasonInvalidValue, ReasonUnavailable}},
		{provider: ProviderRunPod, sourceID: "shared-status", status: DispositionAccepted},
		{provider: ProviderRunPod, sourceID: "shared-status", status: DispositionRejected, reasons: []ReasonCode{ReasonUnavailable}},
		{provider: ProviderRunPod, sourceID: "source-a", status: DispositionRejected, reasons: []ReasonCode{ReasonUnavailable}},
		{provider: ProviderRunPod, sourceID: "source-b", status: DispositionRejected, reasons: []ReasonCode{ReasonUnavailable}},
	}
	if len(got) != len(want) {
		t.Fatalf("SortDispositions() length = %d, want %d", len(got), len(want))
	}
	for index, expected := range want {
		actual := got[index]
		if actual.Provider != expected.provider || actual.SourceID != expected.sourceID || actual.Status != expected.status {
			t.Errorf("SortDispositions()[%d] identity = (%q, %q, %q), want (%q, %q, %q)", index, actual.Provider, actual.SourceID, actual.Status, expected.provider, expected.sourceID, expected.status)
		}
		var actualReasons []ReasonCode
		for _, reason := range actual.Reasons {
			actualReasons = append(actualReasons, reason.Code)
		}
		if !reflect.DeepEqual(actualReasons, expected.reasons) {
			t.Errorf("SortDispositions()[%d] reasons = %v, want %v", index, actualReasons, expected.reasons)
		}
	}
	if got[0].Offer == nil || got[0].Offer.ProviderSourceTime == nil || got[3].Offer == nil || got[3].Offer.ProviderSourceTime == nil {
		t.Fatal("SortDispositions() accepted offer data is incomplete")
	}
	got[0].Offer.Billing.Components[0].Name = "mutated component"
	got[0].Offer.Warnings[0].Message = "mutated warning"
	mutatedSourceTime := got[0].Offer.ProviderSourceTime.Add(time.Hour)
	*got[0].Offer.ProviderSourceTime = mutatedSourceTime
	got[3].Offer.Billing.Components[0].Name = "mutated runpod component"
	got[3].Offer.Warnings[0].Message = "mutated runpod warning"
	mutatedRunPodSourceTime := got[3].Offer.ProviderSourceTime.Add(time.Hour)
	*got[3].Offer.ProviderSourceTime = mutatedRunPodSourceTime
	got[1].Reasons[1].Message = "mutated price reason"
	got[2].Reasons[1].Message = "mutated availability reason"
	got[4].Reasons[0].Message = "mutated status reason"
	if !reflect.DeepEqual(in, original) {
		t.Error("SortDispositions() result aliases or mutates nested input")
	}
}

func TestCoverageValidate(t *testing.T) {
	baseline := Coverage{Market: "public catalog", Complete: false, Truncated: false}
	if err := baseline.Validate(); err != nil {
		t.Fatalf("baseline Coverage.Validate() error = %v, want nil", err)
	}

	complete := Coverage{
		Market:   "public catalog",
		Filters:  []string{"available", "gpu"},
		Limit:    100,
		Complete: true,
		Notes: []Reason{
			{Code: ReasonCoverageLimited, Message: "permanent exclusions documented"},
		},
	}
	if err := complete.Validate(); err != nil {
		t.Fatalf("complete Coverage.Validate() error = %v, want nil", err)
	}

	truncated := Coverage{
		Market:    "public catalog",
		Complete:  false,
		Truncated: true,
		Notes: []Reason{
			{Code: ReasonResultsTruncated, Message: "result limit reached"},
		},
	}
	if err := truncated.Validate(); err != nil {
		t.Fatalf("truncated Coverage.Validate() error = %v, want nil", err)
	}

	tests := []struct {
		name     string
		coverage Coverage
		wantErr  string
	}{
		{name: "empty market", coverage: Coverage{}, wantErr: "coverage.market must be a safe nonempty string"},
		{name: "unsafe market", coverage: Coverage{Market: "bad\nmarket"}, wantErr: "coverage.market must be a safe nonempty string"},
		{name: "unsorted filters", coverage: Coverage{Market: "catalog", Filters: []string{"gpu", "available"}}, wantErr: "coverage.filters must be sorted and unique"},
		{name: "duplicate filters", coverage: Coverage{Market: "catalog", Filters: []string{"gpu", "gpu"}}, wantErr: "coverage.filters must be sorted and unique"},
		{name: "unsafe filter", coverage: Coverage{Market: "catalog", Filters: []string{"bad\nfilter"}}, wantErr: "coverage.filters must contain safe nonempty strings"},
		{name: "negative limit", coverage: Coverage{Market: "catalog", Limit: -1}, wantErr: "coverage.limit must be nonnegative"},
		{name: "unsorted notes", coverage: Coverage{Market: "catalog", Notes: []Reason{{Code: ReasonUnavailable, Message: "unavailable"}, {Code: ReasonCoverageLimited, Message: "limited"}}}, wantErr: "coverage.notes must be sorted"},
		{name: "complete and truncated", coverage: Coverage{Market: "catalog", Complete: true, Truncated: true, Notes: []Reason{{Code: ReasonResultsTruncated, Message: "truncated"}}}, wantErr: "coverage cannot be complete and truncated"},
		{name: "truncated without note", coverage: Coverage{Market: "catalog", Truncated: true}, wantErr: "truncated coverage must include a results_truncated note"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.coverage.Validate()
			if err == nil || err.Error() != test.wantErr {
				t.Errorf("Coverage.Validate() error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestOfferJSONPreservesContract(t *testing.T) {
	offer := validOffer()
	data, err := json.Marshal(offer)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	assertJSONField(t, fields, "provider_offer_id_synthetic", "true")
	assertJSONField(t, fields, "observed_at", `"2026-07-14T11:00:00Z"`)
	assertJSONField(t, fields, "provider_source_time", `"2026-07-14T10:59:00Z"`)

	var billing map[string]json.RawMessage
	if err := json.Unmarshal(fields["billing"], &billing); err != nil {
		t.Fatalf("billing JSON error = %v", err)
	}
	assertJSONField(t, billing, "quoted_usd_per_hour", `"0.35"`)
	assertJSONField(t, billing, "minimum_charge_usd", `"0.01"`)

	var warnings []map[string]json.RawMessage
	if err := json.Unmarshal(fields["warnings"], &warnings); err != nil {
		t.Fatalf("warnings JSON error = %v", err)
	}
	assertJSONField(t, warnings[0], "code", `"coverage_limited"`)

	minimal := validOffer()
	minimal.Region = ""
	minimal.CountryCode = ""
	minimal.LocationLabel = ""
	minimal.GPUMemoryGiB = 0
	minimal.ProviderSourceTime = nil
	minimal.Warnings = nil
	minimal.Resources = Resources{}
	minimal.Billing.Components = nil
	minimal.Billing.MinimumChargeUSD = ""
	minimal.Billing.BillingIncrementSeconds = 0
	minimal.Billing.MinimumBillableSeconds = 0
	data, err = json.Marshal(minimal)
	if err != nil {
		t.Fatalf("json.Marshal(minimal) error = %v", err)
	}
	fields = nil
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatalf("json.Unmarshal(minimal) error = %v", err)
	}
	for _, absent := range []string{"region", "country_code", "location_label", "gpu_memory_gib", "provider_source_time", "warnings"} {
		if _, ok := fields[absent]; ok {
			t.Errorf("optional JSON field %q is present", absent)
		}
	}
	assertJSONField(t, fields, "provider_offer_id_synthetic", "true")
}

func TestValidationErrorsDoNotEchoProviderContent(t *testing.T) {
	const controlled = "private-provider-value"
	offer := validOffer()
	offer.ProviderOfferID = controlled + "\n"
	err := offer.Validate()
	if err == nil {
		t.Fatal("Offer.Validate() error = nil, want non-nil")
	}
	if strings.Contains(err.Error(), controlled) {
		t.Fatal("Offer.Validate() error echoed provider-controlled content")
	}
}

func assertJSONField(t *testing.T, fields map[string]json.RawMessage, name, want string) {
	t.Helper()
	got, ok := fields[name]
	if !ok {
		t.Fatalf("JSON field %q is absent", name)
	}
	if string(got) != want {
		t.Errorf("JSON field %q = %s, want %s", name, got, want)
	}
}

func deepCopyOfferForTest(offer Offer) Offer {
	copy := offer
	copy.Billing.Components = append([]PriceComponent(nil), offer.Billing.Components...)
	copy.Warnings = append([]Reason(nil), offer.Warnings...)
	if offer.ProviderSourceTime != nil {
		sourceTime := *offer.ProviderSourceTime
		copy.ProviderSourceTime = &sourceTime
	}
	return copy
}

func deepCopyDispositionsForTest(dispositions []Disposition) []Disposition {
	copy := make([]Disposition, len(dispositions))
	for index, disposition := range dispositions {
		copy[index] = disposition
		copy[index].Reasons = append([]Reason(nil), disposition.Reasons...)
		if disposition.Offer != nil {
			offer := deepCopyOfferForTest(*disposition.Offer)
			copy[index].Offer = &offer
		}
	}
	return copy
}
