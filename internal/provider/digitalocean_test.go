package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const digitalOceanTestToken = "synthetic-token"

func TestDigitalOceanExactRequestAndNormalizesTwoRegions(t *testing.T) {
	t.Parallel()

	observed := time.Date(2026, time.July, 14, 12, 34, 56, 987, time.FixedZone("synthetic", 2*60*60))
	var requests atomic.Int32
	server := newDigitalOceanTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", r.Method)
		}
		if r.URL.RequestURI() != "/v2/sizes?per_page=200&page=1" {
			t.Errorf("request URI = %q", r.URL.RequestURI())
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+digitalOceanTestToken {
			t.Errorf("authorization = %q", got)
		}
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Errorf("accept = %q", got)
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
		writeDigitalOceanJSON(t, w, `{
			"sizes": [{
				"slug": "gpu-h100-synthetic",
				"gpu_info": {"count": 2, "model": "Synthetic H100", "vram": {"amount": 80, "unit": "gib"}},
				"available": true,
				"regions": ["synthetic-2", "synthetic-1"],
				"vcpus": 24,
				"memory": 131072,
				"disk": 720,
				"transfer": 11.25,
				"price_hourly": 0.00743999984115362
			}],
			"links": {"pages": {}}
		}`)
	})

	adapter := newDigitalOcean(server.URL, digitalOceanTestToken, server.Client(), func() time.Time { return observed })
	if got := adapter.Provider(); got != ProviderDigitalOcean {
		t.Fatalf("provider = %q", got)
	}

	snapshot, err := adapter.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want 1", requests.Load())
	}
	if !snapshot.Coverage.Complete || snapshot.Coverage.Truncated {
		t.Fatalf("coverage = %+v", snapshot.Coverage)
	}
	if err := snapshot.Coverage.Validate(); err != nil {
		t.Fatalf("coverage validation: %v", err)
	}
	if len(snapshot.Dispositions) != 2 {
		t.Fatalf("dispositions = %d, want 2", len(snapshot.Dispositions))
	}

	wantIDs := []string{"gpu-h100-synthetic:synthetic-1", "gpu-h100-synthetic:synthetic-2"}
	for index, disposition := range snapshot.Dispositions {
		if err := disposition.Validate(); err != nil {
			t.Fatalf("disposition %d validation: %v", index, err)
		}
		if disposition.Status != DispositionAccepted || disposition.SourceID != wantIDs[index] || disposition.Offer == nil {
			t.Fatalf("disposition %d = %+v", index, disposition)
		}
		offer := disposition.Offer
		if !offer.ProviderOfferIDSynthetic || offer.GPUModel != "Synthetic H100" || offer.GPUCount != 2 || offer.GPUMemoryGiB != 80 {
			t.Errorf("GPU fields = %+v", offer)
		}
		if offer.Region != strings.TrimPrefix(wantIDs[index], "gpu-h100-synthetic:") || offer.Availability != AvailabilityAvailable {
			t.Errorf("location fields = %+v", offer)
		}
		if offer.Resources.VCPUs != 24 || offer.Resources.HostMemoryGiB != 128 || offer.Resources.StorageGiB != 720 || offer.Resources.IncludedTransferTB != 11.25 {
			t.Errorf("resources = %+v", offer.Resources)
		}
		wantBilling := Billing{
			Mode:                    "on_demand",
			QuotedUSDPerHour:        "0.00743999984115362",
			RateScope:               RateScopeTotal,
			BillingIncrementSeconds: 1,
			MinimumBillableSeconds:  60,
			MinimumChargeUSD:        "0.01",
		}
		if !reflect.DeepEqual(offer.Billing, wantBilling) {
			t.Errorf("billing = %+v, want %+v", offer.Billing, wantBilling)
		}
		if offer.SourceURL != digitalOceanSourceURL {
			t.Errorf("source URL = %q", offer.SourceURL)
		}
		if !offer.ObservedAt.Equal(observed) || offer.ObservedAt.Location() != time.UTC {
			t.Errorf("observed_at = %v", offer.ObservedAt)
		}
	}
}

func TestDigitalOceanRejectsInvalidSizes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		size  string
		code  ReasonCode
		field string
	}{
		{name: "CPU size", size: `{"slug":"cpu-synthetic","available":true,"regions":["synthetic-1"],"vcpus":1,"memory":1024,"disk":10,"transfer":1,"price_hourly":1}`, code: ReasonNotGPU},
		{name: "unavailable", size: validDigitalOceanSize(`"available":false`), code: ReasonUnavailable},
		{name: "no regions", size: validDigitalOceanSize(`"regions":[]`), code: ReasonRegionUnknown},
		{name: "missing slug", size: validDigitalOceanSize(`"slug":""`), code: ReasonMissingRequired},
		{name: "unsafe slug", size: validDigitalOceanSize(`"slug":"bad\nslug"`), code: ReasonInvalidValue},
		{name: "missing model", size: validDigitalOceanSize(`"gpu_info":{"count":1,"model":"","vram":{"amount":24,"unit":"gib"}}`), code: ReasonMissingRequired},
		{name: "unsafe model", size: validDigitalOceanSize(`"gpu_info":{"count":1,"model":"bad\nmodel","vram":{"amount":24,"unit":"gib"}}`), code: ReasonInvalidValue},
		{name: "missing count", size: validDigitalOceanSize(`"gpu_info":{"model":"Synthetic GPU","vram":{"amount":24,"unit":"gib"}}`), code: ReasonMissingRequired},
		{name: "invalid count", size: validDigitalOceanSize(`"gpu_info":{"count":0,"model":"Synthetic GPU","vram":{"amount":24,"unit":"gib"}}`), code: ReasonInvalidValue},
		{name: "missing VRAM", size: validDigitalOceanSize(`"gpu_info":{"count":1,"model":"Synthetic GPU","vram":{"unit":"gib"}}`), code: ReasonMissingRequired},
		{name: "invalid VRAM", size: validDigitalOceanSize(`"gpu_info":{"count":1,"model":"Synthetic GPU","vram":{"amount":0,"unit":"gib"}}`), code: ReasonInvalidValue},
		{name: "wrong VRAM unit", size: validDigitalOceanSize(`"gpu_info":{"count":1,"model":"Synthetic GPU","vram":{"amount":24,"unit":"GiB"}}`), code: ReasonInvalidValue},
		{name: "missing price", size: validDigitalOceanSize(`"price_hourly":null`), code: ReasonPriceMissing},
		{name: "invalid price", size: validDigitalOceanSize(`"price_hourly":0`), code: ReasonInvalidValue},
		{name: "invalid region", size: validDigitalOceanSize(`"regions":["bad\nregion"]`), code: ReasonInvalidValue},
		{name: "invalid vcpus", size: validDigitalOceanSize(`"vcpus":0`), code: ReasonInvalidValue},
		{name: "invalid memory", size: validDigitalOceanSize(`"memory":0`), code: ReasonInvalidValue},
		{name: "invalid disk", size: validDigitalOceanSize(`"disk":0`), code: ReasonInvalidValue},
		{name: "invalid transfer", size: validDigitalOceanSize(`"transfer":0`), code: ReasonInvalidValue},
		{name: "fractional vcpus", size: validDigitalOceanSize(`"vcpus":1.5`), code: ReasonInvalidValue, field: "vcpus"},
		{name: "fractional memory", size: validDigitalOceanSize(`"memory":1024.5`), code: ReasonInvalidValue, field: "memory"},
		{name: "fractional disk", size: validDigitalOceanSize(`"disk":200.5`), code: ReasonInvalidValue, field: "disk"},
		{name: "fractional VRAM", size: validDigitalOceanSize(`"gpu_info":{"count":1,"model":"Synthetic GPU","vram":{"amount":24.5,"unit":"gib"}}`), code: ReasonInvalidValue, field: "gpu_info.vram.amount"},
		{name: "exponent vcpus", size: validDigitalOceanSize(`"vcpus":1e1`), code: ReasonInvalidValue, field: "vcpus"},
		{name: "inexact vcpus integer", size: validDigitalOceanSize(`"vcpus":9007199254740993`), code: ReasonInvalidValue, field: "vcpus"},
		{name: "overflowing memory integer", size: validDigitalOceanSize(`"memory":9223372036854775808`), code: ReasonInvalidValue, field: "memory"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := newDigitalOceanTLSServer(t, func(w http.ResponseWriter, _ *http.Request) {
				writeDigitalOceanJSON(t, w, `{"sizes":[`+test.size+`],"links":{"pages":{}}}`)
			})
			adapter := newDigitalOcean(server.URL, digitalOceanTestToken, server.Client(), fixedDigitalOceanClock)
			snapshot, err := adapter.Discover(context.Background())
			if err != nil {
				t.Fatalf("Discover() error = %v", err)
			}
			if len(snapshot.Dispositions) != 1 {
				t.Fatalf("dispositions = %d, want 1", len(snapshot.Dispositions))
			}
			disposition := snapshot.Dispositions[0]
			if disposition.Status != DispositionRejected || !hasReasonCode(disposition.Reasons, test.code) {
				t.Fatalf("disposition = %+v, want rejection %q", disposition, test.code)
			}
			if test.field != "" && (len(disposition.Reasons) != 1 || disposition.Reasons[0].Code != ReasonInvalidValue || disposition.Reasons[0].Field != test.field) {
				t.Fatalf("reasons = %+v, want one invalid_value reason for %q", disposition.Reasons, test.field)
			}
			if err := disposition.Validate(); err != nil {
				t.Fatalf("rejection validation: %v", err)
			}
		})
	}
}

func TestDigitalOceanIgnoresUnknownFieldsAndRejectsSecondJSONValue(t *testing.T) {
	t.Parallel()

	t.Run("unknown fields", func(t *testing.T) {
		t.Parallel()
		server := newDigitalOceanTLSServer(t, func(w http.ResponseWriter, _ *http.Request) {
			writeDigitalOceanJSON(t, w, `{"unknown":{"nested":true},"sizes":[`+validDigitalOceanSize(`"future":"ignored"`)+`],"links":{"unknown":1,"pages":{}}}`)
		})
		snapshot, err := newDigitalOcean(server.URL, digitalOceanTestToken, server.Client(), fixedDigitalOceanClock).Discover(context.Background())
		if err != nil || len(snapshot.Dispositions) != 1 || snapshot.Dispositions[0].Status != DispositionAccepted {
			t.Fatalf("snapshot = %+v, error = %v", snapshot, err)
		}
	})

	t.Run("second JSON value", func(t *testing.T) {
		t.Parallel()
		server := newDigitalOceanTLSServer(t, func(w http.ResponseWriter, _ *http.Request) {
			writeDigitalOceanJSON(t, w, `{"sizes":[],"links":{"pages":{}}} {"sizes":[]}`)
		})
		snapshot, err := newDigitalOcean(server.URL, digitalOceanTestToken, server.Client(), fixedDigitalOceanClock).Discover(context.Background())
		assertDigitalOceanFailure(t, snapshot, err, FailureInvalidResponse, 0, 0)
	})
}

func TestDigitalOceanRejectsDuplicateOfferIdentity(t *testing.T) {
	t.Parallel()

	server := newDigitalOceanTLSServer(t, func(w http.ResponseWriter, _ *http.Request) {
		size := validDigitalOceanSize(`"regions":["synthetic-1","synthetic-2"]`)
		writeDigitalOceanJSON(t, w, `{"sizes":[`+size+`,`+size+`],"links":{"pages":{}}}`)
	})
	snapshot, err := newDigitalOcean(server.URL, digitalOceanTestToken, server.Client(), fixedDigitalOceanClock).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Dispositions) != 4 {
		t.Fatalf("dispositions = %d, want 4", len(snapshot.Dispositions))
	}
	seen := make(map[string]struct{}, len(snapshot.Dispositions))
	accepted := 0
	rejected := 0
	for _, disposition := range snapshot.Dispositions {
		if _, duplicate := seen[disposition.SourceID]; duplicate {
			t.Errorf("duplicate source ID %q", disposition.SourceID)
		}
		seen[disposition.SourceID] = struct{}{}
		switch disposition.Status {
		case DispositionAccepted:
			accepted++
		case DispositionRejected:
			rejected++
			if !hasReasonCode(disposition.Reasons, ReasonInvalidValue) {
				t.Errorf("duplicate rejection reasons = %+v", disposition.Reasons)
			}
		}
	}
	if accepted != 2 || rejected != 2 {
		t.Fatalf("accepted = %d, rejected = %d", accepted, rejected)
	}
}

func TestDigitalOceanPagination(t *testing.T) {
	t.Parallel()

	t.Run("follows valid next URLs and sorts all results", func(t *testing.T) {
		t.Parallel()
		var server *httptest.Server
		var requests atomic.Int32
		server = newDigitalOceanTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
			requests.Add(1)
			switch r.URL.Query().Get("page") {
			case "1":
				writeDigitalOceanJSON(t, w, fmt.Sprintf(`{"sizes":[%s],"links":{"pages":{"next":%q}}}`, validDigitalOceanSize(`"slug":"gpu-z"`), server.URL+"/v2/sizes?page=2&per_page=200"))
			case "2":
				writeDigitalOceanJSON(t, w, `{"sizes":[`+validDigitalOceanSize(`"slug":"gpu-a"`)+`],"links":{"pages":{}}}`)
			default:
				t.Fatalf("unsafe page requested: %q", r.URL.RequestURI())
			}
		})
		snapshot, err := newDigitalOcean(server.URL, digitalOceanTestToken, server.Client(), fixedDigitalOceanClock).Discover(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if requests.Load() != 2 || len(snapshot.Dispositions) != 2 || snapshot.Dispositions[0].SourceID != "gpu-a:synthetic-1" || snapshot.Dispositions[1].SourceID != "gpu-z:synthetic-1" {
			t.Fatalf("requests = %d, dispositions = %+v", requests.Load(), snapshot.Dispositions)
		}
	})

	invalidNext := []struct {
		name string
		next func(string) string
	}{
		{name: "too long", next: func(base string) string {
			return base + "/v2/sizes?per_page=200&page=2&padding=" + strings.Repeat("x", 2048)
		}},
		{name: "http downgrade", next: func(base string) string {
			return strings.Replace(base, "https://", "http://", 1) + "/v2/sizes?per_page=200&page=2"
		}},
		{name: "suffix host", next: func(base string) string {
			parsed, _ := url.Parse(base)
			return "https://" + parsed.Hostname() + ".invalid/v2/sizes?per_page=200&page=2"
		}},
		{name: "alternate port", next: func(base string) string {
			parsed, _ := url.Parse(base)
			return "https://" + parsed.Hostname() + ":1/v2/sizes?per_page=200&page=2"
		}},
		{name: "relative", next: func(string) string { return "/v2/sizes?per_page=200&page=2" }},
		{name: "userinfo", next: func(base string) string {
			return strings.Replace(base, "https://", "https://user@", 1) + "/v2/sizes?per_page=200&page=2"
		}},
		{name: "fragment", next: func(base string) string { return base + "/v2/sizes?per_page=200&page=2#fragment" }},
		{name: "trailing empty fragment", next: func(base string) string {
			return base + "/v2/sizes?per_page=200&page=2#"
		}},
		{name: "opaque", next: func(string) string { return "https:opaque" }},
		{name: "wrong path", next: func(base string) string { return base + "/v2/sizes/extra?per_page=200&page=2" }},
		{name: "escaped path", next: func(base string) string { return base + "/v2/%73izes?per_page=200&page=2" }},
		{name: "extra key", next: func(base string) string { return base + "/v2/sizes?per_page=200&page=2&extra=1" }},
		{name: "duplicate page", next: func(base string) string { return base + "/v2/sizes?per_page=200&page=2&page=3" }},
		{name: "duplicate per page", next: func(base string) string { return base + "/v2/sizes?per_page=200&per_page=200&page=2" }},
		{name: "wrong per page", next: func(base string) string { return base + "/v2/sizes?per_page=100&page=2" }},
		{name: "missing page", next: func(base string) string { return base + "/v2/sizes?per_page=200" }},
		{name: "missing per page", next: func(base string) string { return base + "/v2/sizes?page=2" }},
		{name: "noncanonical page leading zero", next: func(base string) string { return base + "/v2/sizes?per_page=200&page=02" }},
		{name: "noncanonical page plus", next: func(base string) string { return base + "/v2/sizes?per_page=200&page=%2B2" }},
		{name: "page zero", next: func(base string) string { return base + "/v2/sizes?per_page=200&page=0" }},
		{name: "cycle", next: func(base string) string { return base + "/v2/sizes?per_page=200&page=1" }},
		{name: "page over cap", next: func(base string) string { return base + "/v2/sizes?per_page=200&page=21" }},
		{name: "malformed escape", next: func(base string) string { return base + "/v2/sizes?per_page=200&page=%zz" }},
		{name: "semicolon query", next: func(base string) string { return base + "/v2/sizes?per_page=200;page=2" }},
	}

	for _, test := range invalidNext {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var requests atomic.Int32
			var server *httptest.Server
			server = newDigitalOceanTLSServer(t, func(w http.ResponseWriter, _ *http.Request) {
				if requests.Add(1) != 1 {
					t.Fatal("unsafe follow-up request")
				}
				writeDigitalOceanJSON(t, w, fmt.Sprintf(`{"sizes":[%s],"links":{"pages":{"next":%q}}}`, validDigitalOceanSize(""), test.next(server.URL)))
			})
			snapshot, err := newDigitalOcean(server.URL, digitalOceanTestToken, server.Client(), fixedDigitalOceanClock).Discover(context.Background())
			assertDigitalOceanFailure(t, snapshot, err, FailureInvalidResponse, 0, 0)
			if requests.Load() != 1 || len(snapshot.Dispositions) != 1 {
				t.Fatalf("requests = %d, dispositions = %d", requests.Load(), len(snapshot.Dispositions))
			}
		})
	}
}

func TestDigitalOceanHTTPFailuresAndMissingToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		status     int
		resetDelta int64
		code       FailureCode
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, code: FailureAuthentication},
		{name: "rate limited", status: http.StatusTooManyRequests, resetDelta: 37, code: FailureRateLimited},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			now := fixedDigitalOceanClock()
			server := newDigitalOceanTLSServer(t, func(w http.ResponseWriter, _ *http.Request) {
				if test.resetDelta != 0 {
					w.Header().Set("ratelimit-reset", strconv.FormatInt(now.Unix()+test.resetDelta, 10))
				}
				w.WriteHeader(test.status)
			})
			snapshot, err := newDigitalOcean(server.URL, digitalOceanTestToken, server.Client(), func() time.Time { return now }).Discover(context.Background())
			assertDigitalOceanFailure(t, snapshot, err, test.code, test.status, test.resetDelta)
		})
	}

	t.Run("missing token makes no request", func(t *testing.T) {
		t.Parallel()
		var requests atomic.Int32
		server := newDigitalOceanTLSServer(t, func(http.ResponseWriter, *http.Request) {
			requests.Add(1)
		})
		adapter := newDigitalOcean(server.URL, "", server.Client(), fixedDigitalOceanClock)
		snapshot, err := adapter.Discover(context.Background())
		assertDigitalOceanFailure(t, snapshot, err, FailureNotConfigured, 0, 0)
		if requests.Load() != 0 {
			t.Fatalf("requests = %d, want 0", requests.Load())
		}
	})
}

func TestDigitalOceanRetainsPartialResultsThroughDiscoverer(t *testing.T) {
	t.Parallel()

	t.Run("later page error", func(t *testing.T) {
		t.Parallel()
		var server *httptest.Server
		server = newDigitalOceanTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("page") == "1" {
				writeDigitalOceanJSON(t, w, fmt.Sprintf(`{"sizes":[%s],"links":{"pages":{"next":%q}}}`, validDigitalOceanSize(""), server.URL+"/v2/sizes?per_page=200&page=2"))
				return
			}
			w.WriteHeader(http.StatusServiceUnavailable)
		})
		adapter := newDigitalOcean(server.URL, digitalOceanTestToken, server.Client(), fixedDigitalOceanClock)
		discoverer, err := NewDiscoverer(adapter)
		if err != nil {
			t.Fatal(err)
		}
		results := discoverer.Discover(context.Background())
		if len(results) != 1 || results[0].Status != ResultPartial || results[0].Failure == nil || results[0].Failure.Code != FailureProviderUnavailable || len(results[0].Dispositions) != 1 || results[0].Coverage.Complete {
			t.Fatalf("result = %+v", results)
		}
	})

	t.Run("page cap", func(t *testing.T) {
		t.Parallel()
		var server *httptest.Server
		var requests atomic.Int32
		server = newDigitalOceanTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
			requests.Add(1)
			page, err := strconv.Atoi(r.URL.Query().Get("page"))
			if err != nil || page < 1 || page > digitalOceanMaxPages {
				t.Fatalf("page = %q", r.URL.Query().Get("page"))
			}
			next := server.URL + "/v2/sizes?per_page=200&page=" + strconv.Itoa(page+1)
			writeDigitalOceanJSON(t, w, fmt.Sprintf(`{"sizes":[],"links":{"pages":{"next":%q}}}`, next))
		})
		adapter := newDigitalOcean(server.URL, digitalOceanTestToken, server.Client(), fixedDigitalOceanClock)
		discoverer, err := NewDiscoverer(adapter)
		if err != nil {
			t.Fatal(err)
		}
		results := discoverer.Discover(context.Background())
		if requests.Load() != digitalOceanMaxPages || len(results) != 1 || results[0].Status != ResultPartial || results[0].Failure != nil || !results[0].Coverage.Truncated || results[0].Coverage.Complete || !hasReasonCode(results[0].Coverage.Notes, ReasonResultsTruncated) {
			t.Fatalf("requests = %d, result = %+v", requests.Load(), results)
		}
	})
}

func TestDigitalOceanClockCalledOnceAfterFinalResponse(t *testing.T) {
	t.Parallel()

	observed := time.Date(2026, time.July, 14, 8, 9, 10, 0, time.FixedZone("synthetic", -5*60*60))
	var requests atomic.Int32
	var clockCalls atomic.Int32
	var server *httptest.Server
	server = newDigitalOceanTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
		if clockCalls.Load() != 0 {
			t.Error("clock called before pagination finished")
		}
		page := requests.Add(1)
		if page == 1 {
			writeDigitalOceanJSON(t, w, fmt.Sprintf(`{"sizes":[%s],"links":{"pages":{"next":%q}}}`, validDigitalOceanSize(`"slug":"gpu-b"`), server.URL+"/v2/sizes?per_page=200&page=2"))
			return
		}
		writeDigitalOceanJSON(t, w, `{"sizes":[`+validDigitalOceanSize(`"slug":"gpu-a"`)+`],"links":{"pages":{}}}`)
	})
	clock := func() time.Time {
		clockCalls.Add(1)
		return observed
	}
	snapshot, err := newDigitalOcean(server.URL, digitalOceanTestToken, server.Client(), clock).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if clockCalls.Load() != 1 {
		t.Fatalf("clock calls = %d, want 1", clockCalls.Load())
	}
	for _, disposition := range snapshot.Dispositions {
		if disposition.Offer == nil || !disposition.Offer.ObservedAt.Equal(observed) || disposition.Offer.ObservedAt.Location() != time.UTC {
			t.Errorf("offer timestamp = %+v", disposition.Offer)
		}
	}
}

func TestDigitalOceanCoverageIsClonedAndProductionConstructorUsesProductionBase(t *testing.T) {
	t.Parallel()

	adapter := NewDigitalOcean("token", nil, fixedDigitalOceanClock)
	concrete, ok := adapter.(*digitalOceanAdapter)
	if !ok {
		t.Fatalf("adapter type = %T", adapter)
	}
	if concrete.baseURL != digitalOceanAPIBaseURL {
		t.Fatalf("base URL = %q", concrete.baseURL)
	}

	first := adapter.Coverage()
	if first.Market != "self_service_gpu_droplet_sizes" || first.Limit != 4000 || first.Complete || first.Truncated || !hasReasonCode(first.Notes, ReasonCoverageLimited) {
		t.Fatalf("coverage = %+v", first)
	}
	if err := first.Validate(); err != nil {
		t.Fatal(err)
	}
	first.Filters = append(first.Filters, "mutated")
	first.Notes[0].Message = "mutated"
	second := adapter.Coverage()
	if len(second.Filters) != 0 || second.Notes[0].Message == "mutated" {
		t.Fatalf("coverage aliases caller: %+v", second)
	}
}

func newDigitalOceanTLSServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	return server
}

func writeDigitalOceanJSON(t *testing.T, w http.ResponseWriter, body string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if _, err := io.WriteString(w, body); err != nil {
		t.Fatal(err)
	}
}

func validDigitalOceanSize(replacement string) string {
	fields := []string{
		`"slug":"gpu-synthetic"`,
		`"gpu_info":{"count":1,"model":"Synthetic GPU","vram":{"amount":24,"unit":"gib"}}`,
		`"available":true`,
		`"regions":["synthetic-1"]`,
		`"vcpus":8`,
		`"memory":32768`,
		`"disk":200`,
		`"transfer":5`,
		`"price_hourly":1.25`,
	}
	if replacement != "" {
		key := strings.SplitN(replacement, ":", 2)[0]
		for index, field := range fields {
			if strings.SplitN(field, ":", 2)[0] == key {
				fields[index] = replacement
				return "{" + strings.Join(fields, ",") + "}"
			}
		}
		fields = append(fields, replacement)
	}
	return "{" + strings.Join(fields, ",") + "}"
}

func fixedDigitalOceanClock() time.Time {
	return time.Date(2026, time.July, 14, 10, 0, 0, 0, time.UTC)
}

func assertDigitalOceanFailure(t *testing.T, snapshot Snapshot, err error, code FailureCode, status int, retryAfter int64) {
	t.Helper()
	var adapterErr *AdapterError
	if !errors.As(err, &adapterErr) {
		t.Fatalf("error = %v, want AdapterError", err)
	}
	if adapterErr.Provider != ProviderDigitalOcean || adapterErr.Code != code || adapterErr.HTTPStatus != status || adapterErr.RetryAfterSeconds != retryAfter {
		t.Fatalf("adapter error = %+v", adapterErr)
	}
	if snapshot.Coverage.Complete {
		t.Fatalf("failure coverage complete: %+v", snapshot.Coverage)
	}
	if err := snapshot.Coverage.Validate(); err != nil {
		t.Fatalf("coverage validation: %v", err)
	}
}
