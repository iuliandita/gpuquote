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

const (
	vastTestToken = "synthetic-vast-token"
	vastTestBody  = `{"limit":100,"type":"on-demand","verified":{"eq":true},"external":{"eq":false},"rentable":{"eq":true},"rented":{"eq":false},"allocated_storage":8,"order":[["dph_total","asc"],["id","asc"]]}`
)

func TestVastExactRequestAndFullNormalization(t *testing.T) {
	t.Parallel()
	observed := time.Date(2033, time.May, 18, 5, 33, 20, 987, time.FixedZone("synthetic", 3600))
	var requests atomic.Int32
	server := newVastTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodPost || r.URL.RequestURI() != "/api/v0/bundles/" {
			t.Errorf("request = %s %s", r.Method, r.URL.RequestURI())
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+vastTestToken {
			t.Errorf("authorization = %q", got)
		}
		if r.Header.Get("Accept") != "application/json" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("headers = %+v", r.Header)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != vastTestBody {
			t.Errorf("body = %s, want %s", body, vastTestBody)
		}
		writeVastJSON(t, w, `{"offers":[`+validVastOffer()+`]}`)
	})
	var clockCalls atomic.Int32
	adapter := newVastAI(server.URL+"/api/v0/bundles/", vastTestToken, server.Client(), func() time.Time {
		clockCalls.Add(1)
		return observed
	})
	snapshot, err := adapter.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 || clockCalls.Load() != 1 {
		t.Fatalf("requests = %d, clock = %d", requests.Load(), clockCalls.Load())
	}
	if got := snapshot.Coverage; !got.Complete || got.Truncated || got.Market != "on_demand_verified_rentable_unrented" || got.Limit != 100 {
		t.Fatalf("coverage = %+v", got)
	}
	if len(snapshot.Dispositions) != 1 || snapshot.Dispositions[0].Offer == nil {
		t.Fatalf("dispositions = %+v", snapshot.Dispositions)
	}
	d := snapshot.Dispositions[0]
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	o := d.Offer
	if d.SourceID != "42" || d.Status != DispositionAccepted || o.Provider != ProviderVastAI || o.ProviderOfferIDSynthetic || o.GPUModel != "Synthetic GPU" || o.GPUCount != 2 || o.GPUMemoryGiB != 24 {
		t.Fatalf("identity/GPU = %+v", d)
	}
	if o.LocationLabel != "Synthetic City, DE" || o.CountryCode != "DE" || o.Availability != AvailabilityAvailable {
		t.Errorf("location = %+v", o)
	}
	wantResources := Resources{VCPUs: 12.5, HostMemoryGiB: 128, StorageGiB: 8, DownloadMBPerSec: 500, UploadMBPerSec: 250}
	if !reflect.DeepEqual(o.Resources, wantResources) {
		t.Errorf("resources = %+v, want %+v", o.Resources, wantResources)
	}
	wantBilling := Billing{Mode: "on_demand", QuotedUSDPerHour: "0.8123456789012345", RateScope: RateScopeTotal, BillingIncrementSeconds: 1, Components: []PriceComponent{{Name: "gpu", USDPerHour: "0.75"}, {Name: "storage", USDPerHour: "0.01"}}}
	if !reflect.DeepEqual(o.Billing, wantBilling) {
		t.Errorf("billing = %+v, want %+v", o.Billing, wantBilling)
	}
	if o.SourceURL != vastSourceURL || !o.ObservedAt.Equal(observed) || o.ObservedAt.Location() != time.UTC {
		t.Errorf("provenance = %+v", o)
	}
	if len(o.Warnings) != 1 || o.Warnings[0].Code != ReasonCredentialScopeUnrestricted || !strings.Contains(o.Warnings[0].Message, "write operations") {
		t.Errorf("warnings = %+v", o.Warnings)
	}
}

func TestVastPriceFallbackComponentsAndTotalRAMWarning(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		offer      string
		wantPrice  string
		components []PriceComponent
		warning    bool
	}{
		{name: "search preferred", offer: validVastOffer(), wantPrice: "0.8123456789012345", components: []PriceComponent{{Name: "gpu", USDPerHour: "0.75"}, {Name: "storage", USDPerHour: "0.01"}}},
		{name: "dph fallback", offer: validVastOffer(`"search":{"gpuCostPerHour":0.700,"diskHour":0.020}`), wantPrice: "0.987654321", components: []PriceComponent{{Name: "gpu", USDPerHour: "0.7"}, {Name: "storage", USDPerHour: "0.02"}}},
		{name: "no search", offer: validVastOffer(`"search":null`), wantPrice: "0.987654321"},
		{name: "RAM mismatch", offer: validVastOffer(`"gpu_total_ram":50000`), wantPrice: "0.8123456789012345", components: []PriceComponent{{Name: "gpu", USDPerHour: "0.75"}, {Name: "storage", USDPerHour: "0.01"}}, warning: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			snapshot := discoverVastBody(t, `{"offers":[`+test.offer+`]}`)
			o := snapshot.Dispositions[0].Offer
			if o == nil || o.Billing.QuotedUSDPerHour != test.wantPrice || !reflect.DeepEqual(o.Billing.Components, test.components) {
				t.Fatalf("offer = %+v", o)
			}
			if hasReasonCode(o.Warnings, ReasonSchemaMismatch) != test.warning {
				t.Fatalf("warnings = %+v", o.Warnings)
			}
		})
	}
}

func TestVastNullOptionalNetworkFieldsAreAbsent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		replacement  string
		wantDownload float64
		wantUpload   float64
	}{
		{name: "download", replacement: `"inet_down":null`, wantUpload: 250},
		{name: "upload", replacement: `"inet_up":null`, wantDownload: 500},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			snapshot := discoverVastBody(t, `{"offers":[`+validVastOffer(test.replacement)+`]}`)
			d := snapshot.Dispositions[0]
			if d.Status != DispositionAccepted || d.Offer == nil {
				t.Fatalf("disposition = %+v", d)
			}
			if got := d.Offer.Resources; got.DownloadMBPerSec != test.wantDownload || got.UploadMBPerSec != test.wantUpload {
				t.Fatalf("resources = %+v", got)
			}
		})
	}
}

func TestVastNullOptionalSearchPriceFieldsAreAbsent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		search     string
		wantPrice  string
		components []PriceComponent
	}{
		{name: "total", search: `"search":{"gpuCostPerHour":0.75,"diskHour":0.01,"totalHour":null}`, wantPrice: "0.987654321", components: []PriceComponent{{Name: "gpu", USDPerHour: "0.75"}, {Name: "storage", USDPerHour: "0.01"}}},
		{name: "GPU component", search: `"search":{"gpuCostPerHour":null,"diskHour":0.01,"totalHour":0.8123456789012345}`, wantPrice: "0.8123456789012345", components: []PriceComponent{{Name: "storage", USDPerHour: "0.01"}}},
		{name: "storage component", search: `"search":{"gpuCostPerHour":0.75,"diskHour":null,"totalHour":0.8123456789012345}`, wantPrice: "0.8123456789012345", components: []PriceComponent{{Name: "gpu", USDPerHour: "0.75"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			snapshot := discoverVastBody(t, `{"offers":[`+validVastOffer(test.search)+`]}`)
			d := snapshot.Dispositions[0]
			if d.Status != DispositionAccepted || d.Offer == nil {
				t.Fatalf("disposition = %+v", d)
			}
			if got := d.Offer.Billing; got.QuotedUSDPerHour != test.wantPrice || !reflect.DeepEqual(got.Components, test.components) {
				t.Fatalf("billing = %+v", got)
			}
		})
	}
}

func TestVastLocationCountryDerivation(t *testing.T) {
	t.Parallel()
	tests := []struct{ location, country string }{{"DE", "DE"}, {"Synthetic City, DE", "DE"}, {"Synthetic City", ""}, {"Synthetic, DE, FR", ""}}
	for index, test := range tests {
		t.Run(strconv.Itoa(index), func(t *testing.T) {
			t.Parallel()
			offer := validVastOffer(`"id":`+strconv.Itoa(100+index), `"geolocation":`+strconv.Quote(test.location))
			snapshot := discoverVastBody(t, `{"offers":[`+offer+`]}`)
			got := snapshot.Dispositions[0].Offer
			if got == nil || got.LocationLabel != test.location || got.CountryCode != test.country {
				t.Fatalf("offer = %+v", got)
			}
		})
	}
}

func TestVastRejectsMalformedOffersIndividually(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		offer string
		code  ReasonCode
	}{
		{name: "null item", offer: `null`, code: ReasonMissingRequired},
		{name: "missing id", offer: validVastOfferWithout("id"), code: ReasonMissingRequired},
		{name: "fractional id", offer: validVastOffer(`"id":42.5`), code: ReasonInvalidValue},
		{name: "missing GPU", offer: validVastOffer(`"gpu_name":null`), code: ReasonMissingRequired},
		{name: "unsafe GPU", offer: validVastOffer(`"gpu_name":"bad\nGPU"`), code: ReasonInvalidValue},
		{name: "missing GPU count", offer: validVastOffer(`"num_gpus":null`), code: ReasonMissingRequired},
		{name: "zero GPU count", offer: validVastOffer(`"num_gpus":0`), code: ReasonInvalidValue},
		{name: "missing GPU RAM", offer: validVastOffer(`"gpu_ram":null`), code: ReasonMissingRequired},
		{name: "zero GPU RAM", offer: validVastOffer(`"gpu_ram":0`), code: ReasonInvalidValue},
		{name: "missing GPU fraction", offer: validVastOffer(`"gpu_frac":null`), code: ReasonMissingRequired},
		{name: "fractional GPU", offer: validVastOffer(`"gpu_frac":0.5`), code: ReasonInvalidValue},
		{name: "missing location", offer: validVastOfferWithout("geolocation"), code: ReasonMissingRequired},
		{name: "unsafe location", offer: validVastOffer(`"geolocation":"bad\nlocation"`), code: ReasonInvalidValue},
		{name: "zero effective CPU", offer: validVastOffer(`"cpu_cores_effective":0`), code: ReasonInvalidValue},
		{name: "zero host RAM", offer: validVastOffer(`"cpu_ram":0`), code: ReasonInvalidValue},
		{name: "zero network", offer: validVastOffer(`"inet_down":0`), code: ReasonInvalidValue},
		{name: "missing price", offer: validVastOffer(`"search":null`, `"dph_total":null`), code: ReasonPriceMissing},
		{name: "zero total price", offer: validVastOffer(`"search":null`, `"dph_total":0`), code: ReasonInvalidValue},
		{name: "malformed search money", offer: validVastOffer(`"search":{"totalHour":"private","gpuCostPerHour":0.75,"diskHour":0.01}`), code: ReasonInvalidValue},
		{name: "missing rentable", offer: validVastOffer(`"rentable":null`), code: ReasonMissingRequired},
		{name: "not rentable", offer: validVastOffer(`"rentable":false`), code: ReasonUnavailable},
		{name: "missing rented", offer: validVastOffer(`"rented":null`), code: ReasonMissingRequired},
		{name: "rented", offer: validVastOffer(`"rented":true`), code: ReasonUnavailable},
		{name: "external", offer: validVastOffer(`"external":true`), code: ReasonUnavailable},
		{name: "missing verification", offer: validVastOffer(`"verification":null`), code: ReasonMissingRequired},
		{name: "unverified", offer: validVastOffer(`"verification":"unverified"`), code: ReasonUnavailable},
		{name: "invalid total GPU RAM", offer: validVastOffer(`"gpu_total_ram":0`), code: ReasonInvalidValue},
		{name: "invalid end", offer: validVastOffer(`"end_date":0`), code: ReasonInvalidValue},
		{name: "expired", offer: validVastOffer(`"end_date":2000000000`), code: ReasonUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			snapshot := discoverVastBody(t, `{"offers":[`+test.offer+`]}`)
			if len(snapshot.Dispositions) != 1 {
				t.Fatalf("dispositions = %+v", snapshot.Dispositions)
			}
			d := snapshot.Dispositions[0]
			if d.Status != DispositionRejected || !hasReasonCode(d.Reasons, test.code) || d.Validate() != nil || d.SourceID == "" {
				t.Fatalf("disposition = %+v", d)
			}
			for _, reason := range d.Reasons {
				if strings.Contains(reason.Message, "private") {
					t.Fatalf("reason leaked input: %+v", reason)
				}
			}
		})
	}
}

func TestVastIgnoresWebpageAndOptionalExternalEndDate(t *testing.T) {
	t.Parallel()
	for _, replacement := range []string{`"webpage":{"private":"ignored"}`, `"webpage":null`, `"external":null`, `"external":false`, `"end_date":null`, `"end_date":2000000001`} {
		snapshot := discoverVastBody(t, `{"offers":[`+validVastOffer(replacement)+`]}`)
		if snapshot.Dispositions[0].Status != DispositionAccepted {
			t.Fatalf("replacement %s: %+v", replacement, snapshot.Dispositions[0])
		}
	}
}

func TestVastDuplicateIDsUseSafeFallback(t *testing.T) {
	t.Parallel()
	body := `{"offers":[` + validVastOffer(`"id":7`) + `,` + validVastOffer(`"id":7`) + `]}`
	snapshot := discoverVastBody(t, body)
	if len(snapshot.Dispositions) != 2 {
		t.Fatal(snapshot.Dispositions)
	}
	if snapshot.Dispositions[0].SourceID != "7" || snapshot.Dispositions[0].Status != DispositionAccepted || snapshot.Dispositions[1].SourceID != "item-2" || snapshot.Dispositions[1].Status != DispositionRejected || !hasReasonCode(snapshot.Dispositions[1].Reasons, ReasonSchemaMismatch) {
		t.Fatalf("dispositions = %+v", snapshot.Dispositions)
	}
	discoverer, err := NewDiscoverer(newVastFixtureAdapter(t, body))
	if err != nil {
		t.Fatal(err)
	}
	results := discoverer.Discover(context.Background())
	if len(results) != 1 || results[0].Status != ResultSuccess || results[0].Failure != nil || len(results[0].Dispositions) != 2 {
		t.Fatalf("result = %+v", results)
	}
}

func TestVastEnvelopeAndFraming(t *testing.T) {
	t.Parallel()
	for _, body := range []string{`{}`, `{"offers":null}`, `{"offers":{}}`, `{"offers":[]} {"offers":[]}`} {
		t.Run(strconv.Quote(body), func(t *testing.T) {
			t.Parallel()
			adapter := newVastFixtureAdapter(t, body)
			snapshot, err := adapter.Discover(context.Background())
			assertVastFailure(t, snapshot, err, FailureInvalidResponse, 0)
		})
	}
	t.Run("empty and unknown", func(t *testing.T) {
		t.Parallel()
		snapshot := discoverVastBody(t, `{"unknown":true,"offers":[]}`)
		if !snapshot.Coverage.Complete || len(snapshot.Dispositions) != 0 {
			t.Fatal(snapshot)
		}
	})
}

func TestVastHTTPFailuresNoRetryOrBodyLeak(t *testing.T) {
	t.Parallel()
	tests := []struct {
		status int
		code   FailureCode
	}{{400, FailureRequest}, {401, FailureAuthentication}, {403, FailureAuthentication}, {404, FailureAuthentication}, {429, FailureRateLimited}, {500, FailureProviderUnavailable}}
	for _, test := range tests {
		t.Run(strconv.Itoa(test.status), func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			server := newVastTLSServer(t, func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				body := `{"private":"body"}`
				w.Header().Set("Content-Type", "application/json")
				if test.status == http.StatusTooManyRequests {
					body = "synthetic-private-rate-limit-body"
					w.Header().Set("Content-Type", "text/plain")
				}
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, body)
			})
			snapshot, err := newVastAI(server.URL, vastTestToken, server.Client(), fixedVastClock).Discover(context.Background())
			assertVastFailure(t, snapshot, err, test.code, test.status)
			if calls.Load() != 1 || strings.Contains(err.Error(), "private") {
				t.Fatalf("calls=%d error=%v", calls.Load(), err)
			}
		})
	}
}

func TestVastMissingTokenMakesNoRequest(t *testing.T) {
	t.Parallel()
	var requests atomic.Int32
	server := newVastTLSServer(t, func(http.ResponseWriter, *http.Request) { requests.Add(1) })
	snapshot, err := newVastAI(server.URL, "", server.Client(), fixedVastClock).Discover(context.Background())
	assertVastFailure(t, snapshot, err, FailureNotConfigured, 0)
	if requests.Load() != 0 {
		t.Fatalf("requests = %d", requests.Load())
	}
}

func TestVastCoverageLimitAndDiscovererStatuses(t *testing.T) {
	t.Parallel()
	for _, count := range []int{99, 100} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			t.Parallel()
			items := make([]string, count)
			for index := range items {
				items[index] = validVastOffer(`"id":` + strconv.Itoa(index+1))
			}
			adapter := newVastFixtureAdapter(t, `{"offers":[`+strings.Join(items, ",")+`]}`)
			discoverer, err := NewDiscoverer(adapter)
			if err != nil {
				t.Fatal(err)
			}
			results := discoverer.Discover(context.Background())
			wantStatus := ResultSuccess
			if count == 100 {
				wantStatus = ResultPartial
			}
			if len(results) != 1 || results[0].Status != wantStatus || results[0].Failure != nil || len(results[0].Dispositions) != count {
				t.Fatalf("result = %+v", results)
			}
			if count == 100 && (!results[0].Coverage.Truncated || results[0].Coverage.Complete || !hasReasonCode(results[0].Coverage.Notes, ReasonResultsTruncated)) {
				t.Fatalf("coverage = %+v", results[0].Coverage)
			}
			if count == 99 && (!results[0].Coverage.Complete || results[0].Coverage.Truncated) {
				t.Fatalf("coverage = %+v", results[0].Coverage)
			}
		})
	}
}

func TestVastRejectsMoreThanRequestedLimit(t *testing.T) {
	t.Parallel()
	items := make([]string, 101)
	for index := range items {
		items[index] = validVastOffer(`"id":` + strconv.Itoa(index+1))
	}
	adapter := newVastFixtureAdapter(t, `{"offers":[`+strings.Join(items, ",")+`]}`)
	snapshot, err := adapter.Discover(context.Background())
	assertVastFailure(t, snapshot, err, FailureInvalidResponse, 0)
}

func TestVastCoverageConstructorNilDependenciesAndClassifications(t *testing.T) {
	t.Parallel()
	adapter := NewVastAI("token", nil, fixedVastClock)
	if adapter.Provider() != ProviderVastAI {
		t.Fatal(adapter.Provider())
	}
	first := adapter.Coverage()
	want := Coverage{Market: "on_demand_verified_rentable_unrented", Filters: []string{"external=false", "rentable=true", "rented=false", "verified=true"}, Limit: 100}
	if !reflect.DeepEqual(first, want) || first.Validate() != nil {
		t.Fatalf("coverage = %+v", first)
	}
	first.Filters[0] = "mutated"
	if reflect.DeepEqual(first, adapter.Coverage()) {
		t.Fatal("coverage aliases")
	}
	concrete := adapter.(*vastAdapter)
	if concrete.endpoint != vastSourceURL {
		t.Fatalf("endpoint = %q", concrete.endpoint)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	snapshot, err := NewVastAI(vastTestToken, nil, nil).Discover(ctx)
	assertVastFailure(t, snapshot, err, FailureRequest, 0)

	skipped, _ := NewDiscoverer(NewVastAI("", nil, fixedVastClock))
	if result := skipped.Discover(context.Background())[0]; result.Status != ResultSkipped {
		t.Fatalf("skipped = %+v", result)
	}
	failed, _ := NewDiscoverer(newVastFixtureAdapter(t, `{"offers":{}}`))
	if result := failed.Discover(context.Background())[0]; result.Status != ResultFailed {
		t.Fatalf("failed = %+v", result)
	}
}

func validVastOffer(replacements ...string) string {
	fields := []string{
		`"id":42`, `"gpu_name":"Synthetic GPU"`, `"num_gpus":2`, `"gpu_ram":24576`, `"gpu_total_ram":49152`, `"gpu_frac":1`,
		`"geolocation":"Synthetic City, DE"`, `"cpu_cores_effective":12.5`, `"cpu_cores":64`, `"cpu_ram":131072`, `"disk_space":999`,
		`"inet_down":500`, `"inet_up":250`, `"dph_total":0.987654321`, `"rentable":true`, `"rented":false`, `"external":null`,
		`"verification":"verified"`, `"end_date":null`, `"webpage":"ignored"`,
		`"search":{"gpuCostPerHour":0.75,"diskHour":0.01,"totalHour":0.8123456789012345}`,
	}
	for _, replacement := range replacements {
		key := strings.SplitN(replacement, ":", 2)[0]
		found := false
		for index, field := range fields {
			if strings.SplitN(field, ":", 2)[0] == key {
				fields[index], found = replacement, true
				break
			}
		}
		if !found {
			fields = append(fields, replacement)
		}
	}
	return "{" + strings.Join(fields, ",") + "}"
}

func validVastOfferWithout(field string) string {
	offer := validVastOffer()
	switch field {
	case "id":
		return strings.Replace(offer, `"id":42,`, "", 1)
	case "geolocation":
		return strings.Replace(offer, `,"geolocation":"Synthetic City, DE"`, "", 1)
	default:
		panic("unsupported synthetic field removal")
	}
}

func discoverVastBody(t *testing.T, body string) Snapshot {
	t.Helper()
	adapter := newVastFixtureAdapter(t, body)
	snapshot, err := adapter.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	return snapshot
}

func newVastFixtureAdapter(t *testing.T, body string) Adapter {
	t.Helper()
	server := newVastTLSServer(t, func(w http.ResponseWriter, _ *http.Request) { writeVastJSON(t, w, body) })
	return newVastAI(server.URL+"/api/v0/bundles/", vastTestToken, server.Client(), fixedVastClock)
}

func newVastTLSServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	return server
}

func writeVastJSON(t *testing.T, w http.ResponseWriter, body string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if _, err := io.WriteString(w, body); err != nil {
		t.Error(err)
	}
}

func fixedVastClock() time.Time { return time.Unix(2_000_000_000, 0).UTC() }

func assertVastFailure(t *testing.T, snapshot Snapshot, err error, code FailureCode, status int) {
	t.Helper()
	var adapterErr *AdapterError
	if !errors.As(err, &adapterErr) || adapterErr.Provider != ProviderVastAI || adapterErr.Code != code || adapterErr.HTTPStatus != status {
		t.Fatalf("error = %#v", err)
	}
	if snapshot.Coverage.Complete || snapshot.Coverage.Truncated || snapshot.Coverage.Validate() != nil {
		t.Fatalf("coverage = %+v", snapshot.Coverage)
	}
}
