package provider

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const lambdaTestToken = "synthetic-token"

func TestLambdaExactRequestAndNormalizesTwoRegions(t *testing.T) {
	t.Parallel()

	observed := time.Date(2026, time.July, 14, 12, 34, 56, 987, time.FixedZone("synthetic", 2*60*60))
	var requests atomic.Int32
	var clockCalls atomic.Int32
	server := newLambdaTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", r.Method)
		}
		if r.URL.RequestURI() != "/api/v1/instance-types" {
			t.Errorf("request URI = %q", r.URL.RequestURI())
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+lambdaTestToken {
			t.Errorf("authorization = %q", got)
		}
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Errorf("accept = %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "" {
			t.Errorf("content type = %q, want empty", got)
		}
		if r.ContentLength != 0 {
			t.Errorf("content length = %d, want 0", r.ContentLength)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if len(body) != 0 {
			t.Errorf("request body = %q, want empty", body)
		}
		writeLambdaJSON(t, w, `{
			"data": {
				"gpu.synthetic": {
					"instance_type": {
						"name": "gpu.synthetic",
						"description": "Synthetic accelerator instance",
						"gpu_description": "Synthetic GPU free text 80GB",
						"price_cents_per_hour": 1592,
						"specs": {"vcpus": 24, "memory_gib": 128, "storage_gib": 720, "gpus": 2}
					},
					"regions_with_capacity_available": [
						{"name": "synthetic-z", "description": "Synthetic West"},
						{"name": "synthetic-a", "description": "Synthetic East"}
					],
					"future": {"ignored": true}
				}
			},
			"unknown": true
		}`)
	})
	clock := func() time.Time {
		clockCalls.Add(1)
		return observed
	}
	adapter := newLambda(server.URL+"/api/v1/instance-types", lambdaTestToken, server.Client(), clock)
	if got := adapter.Provider(); got != ProviderLambda {
		t.Fatalf("provider = %q", got)
	}

	snapshot, err := adapter.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want 1", requests.Load())
	}
	if clockCalls.Load() != 1 {
		t.Fatalf("clock calls = %d, want 1", clockCalls.Load())
	}
	wantCoverage := Coverage{Market: "instance_types_available_regions", Complete: true}
	if !reflect.DeepEqual(snapshot.Coverage, wantCoverage) || snapshot.Coverage.Truncated {
		t.Fatalf("coverage = %+v, want %+v", snapshot.Coverage, wantCoverage)
	}
	if err := snapshot.Coverage.Validate(); err != nil {
		t.Fatalf("coverage validation: %v", err)
	}
	if len(snapshot.Dispositions) != 2 {
		t.Fatalf("dispositions = %d, want 2", len(snapshot.Dispositions))
	}

	wantIDs := []string{"gpu.synthetic:synthetic-a", "gpu.synthetic:synthetic-z"}
	wantLabels := []string{"Synthetic East", "Synthetic West"}
	warning := Reason{
		Code:    ReasonCredentialScopeUnrestricted,
		Field:   "credentials",
		Message: "Lambda Cloud API keys have no documented read-only scope",
	}
	for index, disposition := range snapshot.Dispositions {
		if err := disposition.Validate(); err != nil {
			t.Fatalf("disposition %d validation: %v", index, err)
		}
		if disposition.Status != DispositionAccepted || disposition.SourceID != wantIDs[index] || disposition.Offer == nil {
			t.Fatalf("disposition %d = %+v", index, disposition)
		}
		offer := disposition.Offer
		if offer.Provider != ProviderLambda || !offer.ProviderOfferIDSynthetic || offer.GPUModel != "Synthetic GPU free text 80GB" || offer.GPUMemoryGiB != 0 || offer.GPUCount != 2 {
			t.Errorf("GPU fields = %+v", offer)
		}
		if offer.Region != strings.TrimPrefix(wantIDs[index], "gpu.synthetic:") || offer.LocationLabel != wantLabels[index] || offer.Availability != AvailabilityAvailable {
			t.Errorf("location fields = %+v", offer)
		}
		wantResources := Resources{VCPUs: 24, HostMemoryGiB: 128, StorageGiB: 720}
		if !reflect.DeepEqual(offer.Resources, wantResources) {
			t.Errorf("resources = %+v, want %+v", offer.Resources, wantResources)
		}
		wantBilling := Billing{
			Mode:                    "on_demand",
			QuotedUSDPerHour:        "15.92",
			RateScope:               RateScopeTotal,
			BillingIncrementSeconds: 60,
		}
		if !reflect.DeepEqual(offer.Billing, wantBilling) {
			t.Errorf("billing = %+v, want %+v", offer.Billing, wantBilling)
		}
		if offer.SourceURL != lambdaSourceURL {
			t.Errorf("source URL = %q", offer.SourceURL)
		}
		if !offer.ObservedAt.Equal(observed) || offer.ObservedAt.Location() != time.UTC {
			t.Errorf("observed_at = %v", offer.ObservedAt)
		}
		if !reflect.DeepEqual(offer.Warnings, []Reason{warning}) {
			t.Errorf("warnings = %+v", offer.Warnings)
		}
	}

	snapshot.Dispositions[0].Offer.Warnings[0].Message = "mutated"
	if snapshot.Dispositions[1].Offer.Warnings[0].Message != warning.Message {
		t.Fatal("offer warning slices alias each other")
	}
}

func TestLambdaRejectsInvalidIntegerLiterals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		field   string
		value   string
		missing bool
		code    ReasonCode
	}{
		{name: "missing price", field: "price_cents_per_hour", missing: true, code: ReasonPriceMissing},
		{name: "zero price", field: "price_cents_per_hour", value: "0", code: ReasonInvalidValue},
		{name: "negative price", field: "price_cents_per_hour", value: "-1", code: ReasonInvalidValue},
		{name: "fractional price", field: "price_cents_per_hour", value: "1592.5", code: ReasonInvalidValue},
		{name: "exponent price", field: "price_cents_per_hour", value: "1592e0", code: ReasonInvalidValue},
		{name: "quoted price", field: "price_cents_per_hour", value: `"1592"`, code: ReasonInvalidValue},
		{name: "overflowing price", field: "price_cents_per_hour", value: "9223372036854775808", code: ReasonInvalidValue},
		{name: "missing vcpus", field: "vcpus", missing: true, code: ReasonMissingRequired},
		{name: "zero vcpus", field: "vcpus", value: "0", code: ReasonInvalidValue},
		{name: "negative vcpus", field: "vcpus", value: "-1", code: ReasonInvalidValue},
		{name: "missing memory", field: "memory_gib", missing: true, code: ReasonMissingRequired},
		{name: "zero memory", field: "memory_gib", value: "0", code: ReasonInvalidValue},
		{name: "negative memory", field: "memory_gib", value: "-1", code: ReasonInvalidValue},
		{name: "fractional memory", field: "memory_gib", value: "128.5", code: ReasonInvalidValue},
		{name: "inexact memory", field: "memory_gib", value: "9007199254740993", code: ReasonInvalidValue},
		{name: "missing storage", field: "storage_gib", missing: true, code: ReasonMissingRequired},
		{name: "zero storage", field: "storage_gib", value: "0", code: ReasonInvalidValue},
		{name: "negative storage", field: "storage_gib", value: "-1", code: ReasonInvalidValue},
		{name: "exponent storage", field: "storage_gib", value: "720e0", code: ReasonInvalidValue},
		{name: "overflowing storage", field: "storage_gib", value: "18446744073709551616", code: ReasonInvalidValue},
		{name: "missing GPUs", field: "gpus", missing: true, code: ReasonMissingRequired},
		{name: "zero GPUs", field: "gpus", value: "0", code: ReasonInvalidValue},
		{name: "negative GPUs", field: "gpus", value: "-1", code: ReasonInvalidValue},
		{name: "fractional GPUs", field: "gpus", value: "2.0", code: ReasonInvalidValue},
		{name: "overflowing GPUs", field: "gpus", value: "18446744073709551615", code: ReasonInvalidValue},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			item := lambdaItemWithInteger(test.field, test.value, test.missing)
			server := newLambdaTLSServer(t, func(w http.ResponseWriter, _ *http.Request) {
				writeLambdaJSON(t, w, lambdaEnvelope("gpu.synthetic", item))
			})
			snapshot, err := newLambda(server.URL+"/api/v1/instance-types", lambdaTestToken, server.Client(), fixedLambdaClock).Discover(context.Background())
			if err != nil {
				t.Fatalf("Discover() error = %v", err)
			}
			assertLambdaRejection(t, snapshot, "item-1", test.code)
			field := "instance_type.specs." + test.field
			if test.field == "price_cents_per_hour" {
				field = "instance_type.price_cents_per_hour"
			}
			reasons := snapshot.Dispositions[0].Reasons
			if len(reasons) != 1 || reasons[0].Code != test.code || reasons[0].Field != field {
				t.Fatalf("reasons = %+v, want one %q reason for %q", reasons, test.code, field)
			}
		})
	}
}

func TestLambdaRejectsEmptyCapacityAsUnavailable(t *testing.T) {
	t.Parallel()

	server := newLambdaTLSServer(t, func(w http.ResponseWriter, _ *http.Request) {
		writeLambdaJSON(t, w, lambdaEnvelope("gpu.synthetic", validLambdaItem(`"regions_with_capacity_available":[]`)))
	})
	snapshot, err := newLambda(server.URL+"/api/v1/instance-types", lambdaTestToken, server.Client(), fixedLambdaClock).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	assertLambdaRejection(t, snapshot, "item-1", ReasonUnavailable)
}

func TestLambdaRejectsMalformedItemsWithStableSafeIDs(t *testing.T) {
	t.Parallel()

	longName := strings.Repeat("a", 200)
	longRegion := strings.Repeat("b", 100)
	tests := []struct {
		name string
		key  string
		item string
		code ReasonCode
	}{
		{name: "null item", key: "gpu.synthetic", item: "null", code: ReasonMissingRequired},
		{name: "empty key", key: "", item: validLambdaItem(`"instance_type":` + validLambdaInstanceType(`"name":""`)), code: ReasonMissingRequired},
		{name: "unsafe key", key: "bad\nkey", item: validLambdaItem(`"instance_type":` + validLambdaInstanceType(`"name":"bad\nkey"`)), code: ReasonInvalidValue},
		{name: "empty name", key: "gpu.synthetic", item: validLambdaItem(`"instance_type":` + validLambdaInstanceType(`"name":""`)), code: ReasonMissingRequired},
		{name: "unsafe name", key: "gpu.synthetic", item: validLambdaItem(`"instance_type":` + validLambdaInstanceType(`"name":"bad\nname"`)), code: ReasonInvalidValue},
		{name: "empty description", key: "gpu.synthetic", item: validLambdaItem(`"instance_type":` + validLambdaInstanceType(`"description":""`)), code: ReasonMissingRequired},
		{name: "unsafe description", key: "gpu.synthetic", item: validLambdaItem(`"instance_type":` + validLambdaInstanceType(`"description":"bad\ndescription"`)), code: ReasonInvalidValue},
		{name: "empty GPU description", key: "gpu.synthetic", item: validLambdaItem(`"instance_type":` + validLambdaInstanceType(`"gpu_description":""`)), code: ReasonMissingRequired},
		{name: "unsafe GPU description", key: "gpu.synthetic", item: validLambdaItem(`"instance_type":` + validLambdaInstanceType(`"gpu_description":"bad\nGPU"`)), code: ReasonInvalidValue},
		{name: "key name mismatch", key: "gpu.synthetic", item: validLambdaItem(`"instance_type":` + validLambdaInstanceType(`"name":"gpu.other"`)), code: ReasonSchemaMismatch},
		{name: "null price", key: "gpu.synthetic", item: validLambdaItem(`"instance_type":` + validLambdaInstanceType(`"price_cents_per_hour":null`)), code: ReasonPriceMissing},
		{name: "missing specs", key: "gpu.synthetic", item: `{"instance_type":{"name":"gpu.synthetic","description":"Synthetic instance","gpu_description":"Synthetic GPU","price_cents_per_hour":1592},"regions_with_capacity_available":[{"name":"synthetic-1","description":"Synthetic Region"}]}`, code: ReasonMissingRequired},
		{name: "null specs", key: "gpu.synthetic", item: validLambdaItem(`"instance_type":` + validLambdaInstanceType(`"specs":null`)), code: ReasonMissingRequired},
		{name: "missing regions", key: "gpu.synthetic", item: `{"instance_type":` + validLambdaInstanceType("") + `}`, code: ReasonMissingRequired},
		{name: "null regions", key: "gpu.synthetic", item: validLambdaItem(`"regions_with_capacity_available":null`), code: ReasonMissingRequired},
		{name: "null region", key: "gpu.synthetic", item: validLambdaItem(`"regions_with_capacity_available":[null]`), code: ReasonMissingRequired},
		{name: "empty region name", key: "gpu.synthetic", item: validLambdaItem(`"regions_with_capacity_available":[{"name":"","description":"Synthetic Region"}]`), code: ReasonMissingRequired},
		{name: "unsafe region name", key: "gpu.synthetic", item: validLambdaItem(`"regions_with_capacity_available":[{"name":"bad\nregion","description":"Synthetic Region"}]`), code: ReasonInvalidValue},
		{name: "empty region description", key: "gpu.synthetic", item: validLambdaItem(`"regions_with_capacity_available":[{"name":"synthetic-1","description":""}]`), code: ReasonMissingRequired},
		{name: "unsafe region description", key: "gpu.synthetic", item: validLambdaItem(`"regions_with_capacity_available":[{"name":"synthetic-1","description":"bad\ndescription"}]`), code: ReasonInvalidValue},
		{name: "duplicate region", key: "gpu.synthetic", item: validLambdaItem(`"regions_with_capacity_available":[{"name":"synthetic-1","description":"Synthetic Region"},{"name":"synthetic-1","description":"Synthetic Region"}]`), code: ReasonInvalidValue},
		{name: "conflicting region descriptions", key: "gpu.synthetic", item: validLambdaItem(`"regions_with_capacity_available":[{"name":"synthetic-1","description":"Synthetic One"},{"name":"synthetic-1","description":"Synthetic Two"}]`), code: ReasonInvalidValue},
		{name: "invalid synthetic ID", key: longName, item: validLambdaItem(`"instance_type":`+validLambdaInstanceType(`"name":`+strconv.Quote(longName)), `"regions_with_capacity_available":[{"name":`+strconv.Quote(longRegion)+`,"description":"Synthetic Region"}]`), code: ReasonInvalidValue},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := newLambdaTLSServer(t, func(w http.ResponseWriter, _ *http.Request) {
				writeLambdaJSON(t, w, lambdaEnvelope(test.key, test.item))
			})
			snapshot, err := newLambda(server.URL+"/api/v1/instance-types", lambdaTestToken, server.Client(), fixedLambdaClock).Discover(context.Background())
			if err != nil {
				t.Fatalf("Discover() error = %v", err)
			}
			assertLambdaRejection(t, snapshot, "item-1", test.code)
			for _, reason := range snapshot.Dispositions[0].Reasons {
				if strings.Contains(reason.Message, test.key) && test.key != "" {
					t.Fatalf("reason leaks input key: %+v", reason)
				}
			}
		})
	}
}

func TestLambdaInvalidMapNormalizationIsDeterministic(t *testing.T) {
	t.Parallel()

	aItem := validLambdaItem(`"instance_type":` + validLambdaInstanceType(`"name":"a.invalid"`, `"gpu_description":""`))
	mItem := validLambdaItem(`"instance_type":` + validLambdaInstanceType(`"name":"m.invalid"`, `"price_cents_per_hour":0`))
	zItem := validLambdaItem(
		`"instance_type":`+validLambdaInstanceType(`"name":"z.invalid"`),
		`"regions_with_capacity_available":[]`,
	)
	body := `{"data":{"z.invalid":` + zItem + `,"a.invalid":` + aItem + `,"m.invalid":` + mItem + `}}`
	var requests atomic.Int32
	server := newLambdaTLSServer(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writeLambdaJSON(t, w, body)
	})
	adapter := newLambda(server.URL+"/api/v1/instance-types", lambdaTestToken, server.Client(), fixedLambdaClock)

	first, err := adapter.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := adapter.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 {
		t.Fatalf("requests = %d, want 2", requests.Load())
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("discoveries differ:\nfirst:  %+v\nsecond: %+v", first, second)
	}
	if !first.Coverage.Complete || first.Coverage.Truncated || len(first.Dispositions) != 3 {
		t.Fatalf("snapshot = %+v", first)
	}
	wantIDs := []string{"item-1", "item-2", "item-3"}
	wantCodes := []ReasonCode{ReasonMissingRequired, ReasonInvalidValue, ReasonUnavailable}
	rawKeys := []string{"a.invalid", "m.invalid", "z.invalid"}
	for index, disposition := range first.Dispositions {
		if disposition.SourceID != wantIDs[index] || disposition.Status != DispositionRejected || !hasReasonCode(disposition.Reasons, wantCodes[index]) {
			t.Fatalf("disposition %d = %+v", index, disposition)
		}
		if err := disposition.Validate(); err != nil {
			t.Fatalf("disposition %d validation: %v", index, err)
		}
		for _, reason := range disposition.Reasons {
			for _, rawKey := range rawKeys {
				if strings.Contains(reason.Message, rawKey) {
					t.Fatalf("reason leaks raw key %q: %+v", rawKey, reason)
				}
			}
		}
	}
}

func TestLambdaRejectsSyntheticIDDelimiterCollisions(t *testing.T) {
	t.Parallel()

	aItem := validLambdaItem(
		`"instance_type":`+validLambdaInstanceType(
			`"name":"a"`,
			`"description":"Instance: synthetic"`,
			`"gpu_description":"GPU: synthetic"`,
		),
		`"regions_with_capacity_available":[{"name":"b:c","description":"Region: synthetic"}]`,
	)
	abItem := validLambdaItem(
		`"instance_type":`+validLambdaInstanceType(
			`"name":"a:b"`,
			`"description":"Instance: synthetic"`,
			`"gpu_description":"GPU: synthetic"`,
		),
		`"regions_with_capacity_available":[{"name":"c","description":"Region: synthetic"}]`,
	)
	body := `{"data":{"a:b":` + abItem + `,"a":` + aItem + `}}`
	var requests atomic.Int32
	server := newLambdaTLSServer(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writeLambdaJSON(t, w, body)
	})
	discoverer, err := NewDiscoverer(newLambda(server.URL+"/api/v1/instance-types", lambdaTestToken, server.Client(), fixedLambdaClock))
	if err != nil {
		t.Fatal(err)
	}

	first := discoverer.Discover(context.Background())
	second := discoverer.Discover(context.Background())
	if requests.Load() != 2 {
		t.Fatalf("requests = %d, want 2", requests.Load())
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("discoveries differ:\nfirst:  %+v\nsecond: %+v", first, second)
	}
	if len(first) != 1 || first[0].Status != ResultSuccess || first[0].Failure != nil || !first[0].Coverage.Complete || first[0].Coverage.Truncated {
		t.Fatalf("result = %+v, want complete success", first)
	}
	if len(first[0].Dispositions) != 2 {
		t.Fatalf("dispositions = %+v, want two rejections", first[0].Dispositions)
	}
	wantIDs := []string{"item-1", "item-2"}
	wantFields := [][]string{
		{"regions_with_capacity_available.name"},
		{"data.key", "instance_type.name"},
	}
	seen := make(map[string]struct{}, len(wantIDs))
	for index, disposition := range first[0].Dispositions {
		if disposition.SourceID != wantIDs[index] || disposition.Status != DispositionRejected {
			t.Fatalf("disposition %d = %+v", index, disposition)
		}
		if _, duplicate := seen[disposition.SourceID]; duplicate {
			t.Fatalf("duplicate source ID %q", disposition.SourceID)
		}
		seen[disposition.SourceID] = struct{}{}
		if err := disposition.Validate(); err != nil {
			t.Fatalf("disposition %d validation: %v", index, err)
		}
		if len(disposition.Reasons) != len(wantFields[index]) {
			t.Fatalf("reasons %d = %+v", index, disposition.Reasons)
		}
		for reasonIndex, reason := range disposition.Reasons {
			if reason.Code != ReasonInvalidValue || reason.Field != wantFields[index][reasonIndex] {
				t.Fatalf("reason %d.%d = %+v", index, reasonIndex, reason)
			}
			if strings.Contains(reason.Message, "a:b") || strings.Contains(reason.Message, "b:c") {
				t.Fatalf("reason leaks raw identifier: %+v", reason)
			}
		}
	}
}

func TestLambdaEnvelopeValidationAndJSONFraming(t *testing.T) {
	t.Parallel()

	invalid := []struct {
		name string
		body string
	}{
		{name: "missing data", body: `{}`},
		{name: "null data", body: `{"data":null}`},
		{name: "array data", body: `{"data":[]}`},
		{name: "malformed JSON", body: `{"data":`},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := newLambdaTLSServer(t, func(w http.ResponseWriter, _ *http.Request) {
				writeLambdaJSON(t, w, test.body)
			})
			snapshot, err := newLambda(server.URL+"/api/v1/instance-types", lambdaTestToken, server.Client(), fixedLambdaClock).Discover(context.Background())
			assertLambdaFailure(t, snapshot, err, FailureInvalidResponse, 0, 0)
		})
	}

	t.Run("empty data succeeds", func(t *testing.T) {
		t.Parallel()
		server := newLambdaTLSServer(t, func(w http.ResponseWriter, _ *http.Request) {
			writeLambdaJSON(t, w, `{"data":{}}`)
		})
		snapshot, err := newLambda(server.URL+"/api/v1/instance-types", lambdaTestToken, server.Client(), fixedLambdaClock).Discover(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !snapshot.Coverage.Complete || snapshot.Coverage.Truncated || len(snapshot.Dispositions) != 0 {
			t.Fatalf("snapshot = %+v, want complete empty success", snapshot)
		}
	})

	t.Run("unknown fields ignored", func(t *testing.T) {
		t.Parallel()
		server := newLambdaTLSServer(t, func(w http.ResponseWriter, _ *http.Request) {
			writeLambdaJSON(t, w, `{"unknown":true,"data":{"gpu.synthetic":`+validLambdaItem(`"unknown":true`)+`}}`)
		})
		snapshot, err := newLambda(server.URL+"/api/v1/instance-types", lambdaTestToken, server.Client(), fixedLambdaClock).Discover(context.Background())
		if err != nil || len(snapshot.Dispositions) != 1 || snapshot.Dispositions[0].Status != DispositionAccepted {
			t.Fatalf("snapshot = %+v, error = %v", snapshot, err)
		}
	})

	t.Run("second JSON value rejected", func(t *testing.T) {
		t.Parallel()
		server := newLambdaTLSServer(t, func(w http.ResponseWriter, _ *http.Request) {
			writeLambdaJSON(t, w, `{"data":{}} {"data":{}}`)
		})
		snapshot, err := newLambda(server.URL+"/api/v1/instance-types", lambdaTestToken, server.Client(), fixedLambdaClock).Discover(context.Background())
		assertLambdaFailure(t, snapshot, err, FailureInvalidResponse, 0, 0)
	})
}

func TestLambdaHTTPFailuresDoNotLeakBodiesAndDoNotRetry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		status     int
		code       FailureCode
		retryAfter int64
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, code: FailureAuthentication},
		{name: "forbidden", status: http.StatusForbidden, code: FailureAuthentication},
		{name: "rate limited", status: http.StatusTooManyRequests, code: FailureRateLimited, retryAfter: 37},
		{name: "internal error", status: http.StatusInternalServerError, code: FailureProviderUnavailable},
		{name: "unavailable", status: http.StatusServiceUnavailable, code: FailureProviderUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			now := fixedLambdaClock()
			var requests atomic.Int32
			var clockCalls atomic.Int32
			server := newLambdaTLSServer(t, func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				if test.retryAfter != 0 {
					w.Header().Set("Retry-After", now.Add(time.Duration(test.retryAfter)*time.Second).Format(http.TimeFormat))
				}
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, `{"message":"synthetic-secret-suggestion"}`)
			})
			clock := func() time.Time {
				clockCalls.Add(1)
				return now
			}
			snapshot, err := newLambda(server.URL+"/api/v1/instance-types", lambdaTestToken, server.Client(), clock).Discover(context.Background())
			assertLambdaFailure(t, snapshot, err, test.code, test.status, test.retryAfter)
			if requests.Load() != 1 {
				t.Fatalf("requests = %d, want 1", requests.Load())
			}
			if clockCalls.Load() != 1 {
				t.Fatalf("clock calls = %d, want 1", clockCalls.Load())
			}
			if strings.Contains(err.Error(), "synthetic-secret-suggestion") {
				t.Fatalf("error leaks response body: %v", err)
			}
		})
	}
}

func TestLambdaMissingTokenMakesNoRequest(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	var clockCalls atomic.Int32
	server := newLambdaTLSServer(t, func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	})
	adapter := newLambda(server.URL+"/api/v1/instance-types", "", server.Client(), func() time.Time {
		clockCalls.Add(1)
		return fixedLambdaClock()
	})
	snapshot, err := adapter.Discover(context.Background())
	assertLambdaFailure(t, snapshot, err, FailureNotConfigured, 0, 0)
	if requests.Load() != 0 || clockCalls.Load() != 0 {
		t.Fatalf("requests = %d, clock calls = %d, want zero", requests.Load(), clockCalls.Load())
	}
}

func TestLambdaCoverageCloneAndProductionConstructor(t *testing.T) {
	t.Parallel()

	adapter := NewLambda("token", nil, fixedLambdaClock)
	concrete, ok := adapter.(*lambdaAdapter)
	if !ok {
		t.Fatalf("adapter type = %T", adapter)
	}
	if concrete.endpoint != lambdaSourceURL {
		t.Fatalf("endpoint = %q", concrete.endpoint)
	}
	first := adapter.Coverage()
	want := Coverage{Market: "instance_types_available_regions"}
	if !reflect.DeepEqual(first, want) || first.Complete || first.Truncated {
		t.Fatalf("coverage = %+v, want %+v", first, want)
	}
	if err := first.Validate(); err != nil {
		t.Fatal(err)
	}
	first.Filters = append(first.Filters, "mutated")
	first.Notes = append(first.Notes, Reason{Code: ReasonCoverageLimited, Message: "mutated"})
	second := adapter.Coverage()
	if len(second.Filters) != 0 || len(second.Notes) != 0 {
		t.Fatalf("coverage aliases caller: %+v", second)
	}
}

func TestLambdaNilDependenciesAreSafeWithCanceledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	snapshot, err := NewLambda(lambdaTestToken, nil, nil).Discover(ctx)
	assertLambdaFailure(t, snapshot, err, FailureRequest, 0, 0)
}

func TestLambdaDiscovererClassifications(t *testing.T) {
	t.Parallel()

	t.Run("skipped", func(t *testing.T) {
		t.Parallel()
		assertLambdaResultStatus(t, NewLambda("", nil, fixedLambdaClock), ResultSkipped, FailureNotConfigured)
	})
	t.Run("failed", func(t *testing.T) {
		t.Parallel()
		server := newLambdaTLSServer(t, func(w http.ResponseWriter, _ *http.Request) {
			writeLambdaJSON(t, w, `{"data":null}`)
		})
		assertLambdaResultStatus(t, newLambda(server.URL+"/api/v1/instance-types", lambdaTestToken, server.Client(), fixedLambdaClock), ResultFailed, FailureInvalidResponse)
	})
	t.Run("success", func(t *testing.T) {
		t.Parallel()
		server := newLambdaTLSServer(t, func(w http.ResponseWriter, _ *http.Request) {
			writeLambdaJSON(t, w, lambdaEnvelope("gpu.synthetic", validLambdaItem(`"regions_with_capacity_available":[]`)))
		})
		adapter := newLambda(server.URL+"/api/v1/instance-types", lambdaTestToken, server.Client(), fixedLambdaClock)
		discoverer, err := NewDiscoverer(adapter)
		if err != nil {
			t.Fatal(err)
		}
		results := discoverer.Discover(context.Background())
		if len(results) != 1 || results[0].Status != ResultSuccess || results[0].Failure != nil || !results[0].Coverage.Complete || results[0].Coverage.Truncated || len(results[0].Dispositions) != 1 {
			t.Fatalf("results = %+v", results)
		}
		disposition := results[0].Dispositions[0]
		if disposition.Status != DispositionRejected || !hasReasonCode(disposition.Reasons, ReasonUnavailable) {
			t.Fatalf("disposition = %+v", disposition)
		}
		if err := disposition.Validate(); err != nil {
			t.Fatalf("rejection validation: %v", err)
		}
	})
}

func newLambdaTLSServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	return server
}

func writeLambdaJSON(t *testing.T, w http.ResponseWriter, body string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if _, err := io.WriteString(w, body); err != nil {
		t.Fatal(err)
	}
}

func validLambdaItem(replacements ...string) string {
	fields := []string{
		`"instance_type":` + validLambdaInstanceType(""),
		`"regions_with_capacity_available":[{"name":"synthetic-1","description":"Synthetic Region"}]`,
	}
	return replaceLambdaFields(fields, replacements...)
}

func validLambdaInstanceType(replacements ...string) string {
	fields := []string{
		`"name":"gpu.synthetic"`,
		`"description":"Synthetic instance"`,
		`"gpu_description":"Synthetic GPU"`,
		`"price_cents_per_hour":1592`,
		`"specs":` + validLambdaSpecs(""),
	}
	return replaceLambdaFields(fields, replacements...)
}

func validLambdaSpecs(replacement string) string {
	fields := []string{`"vcpus":8`, `"memory_gib":32`, `"storage_gib":200`, `"gpus":1`}
	return replaceLambdaFields(fields, replacement)
}

func lambdaItemWithInteger(field, value string, missing bool) string {
	if field == "price_cents_per_hour" {
		fields := []string{
			`"name":"gpu.synthetic"`,
			`"description":"Synthetic instance"`,
			`"gpu_description":"Synthetic GPU"`,
			`"price_cents_per_hour":1592`,
			`"specs":` + validLambdaSpecs(""),
		}
		if missing {
			fields = removeLambdaField(fields, field)
		} else {
			fields = replaceLambdaField(fields, `"`+field+`":`+value)
		}
		return validLambdaItem(`"instance_type":` + "{" + strings.Join(fields, ",") + "}")
	}

	fields := []string{`"vcpus":8`, `"memory_gib":32`, `"storage_gib":200`, `"gpus":1`}
	if missing {
		fields = removeLambdaField(fields, field)
	} else {
		fields = replaceLambdaField(fields, `"`+field+`":`+value)
	}
	specs := "{" + strings.Join(fields, ",") + "}"
	return validLambdaItem(`"instance_type":` + validLambdaInstanceType(`"specs":`+specs))
}

func removeLambdaField(fields []string, field string) []string {
	wantKey := strconv.Quote(field)
	filtered := make([]string, 0, len(fields)-1)
	for _, candidate := range fields {
		if strings.SplitN(candidate, ":", 2)[0] != wantKey {
			filtered = append(filtered, candidate)
		}
	}
	return filtered
}

func replaceLambdaField(fields []string, replacement string) []string {
	key := strings.SplitN(replacement, ":", 2)[0]
	for index, candidate := range fields {
		if strings.SplitN(candidate, ":", 2)[0] == key {
			fields[index] = replacement
			return fields
		}
	}
	return append(fields, replacement)
}

func replaceLambdaFields(fields []string, replacements ...string) string {
	for _, replacement := range replacements {
		if replacement != "" {
			fields = replaceLambdaField(fields, replacement)
		}
	}
	return "{" + strings.Join(fields, ",") + "}"
}

func lambdaEnvelope(key, item string) string {
	return `{"data":{` + strconv.Quote(key) + `:` + item + `}}`
}

func fixedLambdaClock() time.Time {
	return time.Date(2026, time.July, 14, 10, 0, 0, 0, time.UTC)
}

func assertLambdaRejection(t *testing.T, snapshot Snapshot, sourceID string, code ReasonCode) {
	t.Helper()
	if !snapshot.Coverage.Complete || snapshot.Coverage.Truncated {
		t.Fatalf("coverage = %+v, want complete and nontruncated", snapshot.Coverage)
	}
	if len(snapshot.Dispositions) != 1 {
		t.Fatalf("dispositions = %d, want 1", len(snapshot.Dispositions))
	}
	disposition := snapshot.Dispositions[0]
	if disposition.SourceID != sourceID || disposition.Status != DispositionRejected || !hasReasonCode(disposition.Reasons, code) {
		t.Fatalf("disposition = %+v, want %q rejection %q", disposition, sourceID, code)
	}
	if err := disposition.Validate(); err != nil {
		t.Fatalf("rejection validation: %v", err)
	}
}

func assertLambdaFailure(t *testing.T, snapshot Snapshot, err error, code FailureCode, status int, retryAfter int64) {
	t.Helper()
	var adapterErr *AdapterError
	if !errors.As(err, &adapterErr) {
		t.Fatalf("error = %v, want AdapterError", err)
	}
	if adapterErr.Provider != ProviderLambda || adapterErr.Code != code || adapterErr.HTTPStatus != status || adapterErr.RetryAfterSeconds != retryAfter {
		t.Fatalf("adapter error = %+v", adapterErr)
	}
	if snapshot.Coverage.Complete || snapshot.Coverage.Truncated {
		t.Fatalf("failure coverage = %+v", snapshot.Coverage)
	}
	if err := snapshot.Coverage.Validate(); err != nil {
		t.Fatalf("coverage validation: %v", err)
	}
}

func assertLambdaResultStatus(t *testing.T, adapter Adapter, status ResultStatus, code FailureCode) {
	t.Helper()
	discoverer, err := NewDiscoverer(adapter)
	if err != nil {
		t.Fatal(err)
	}
	results := discoverer.Discover(context.Background())
	if len(results) != 1 || results[0].Status != status {
		t.Fatalf("results = %+v, want status %q", results, status)
	}
	if code == "" {
		if results[0].Failure != nil || len(results[0].Dispositions) != 1 || !results[0].Coverage.Complete {
			t.Fatalf("success result = %+v", results[0])
		}
		return
	}
	if results[0].Failure == nil || results[0].Failure.Code != code || len(results[0].Dispositions) != 0 {
		t.Fatalf("failure result = %+v, want %q", results[0], code)
	}
}
