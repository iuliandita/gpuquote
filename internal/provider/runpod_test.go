package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const runPodTestToken = "synthetic-runpod-token"

func TestRunPodQuerySequenceNormalizesOnlyRejectedCandidates(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	server := newRunPodTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
		requestNumber := int(calls.Add(1))
		body := readRunPodRequest(t, r)
		assertRunPodRequestSafety(t, r, body)
		switch requestNumber {
		case 1:
			assertRunPodInitialQuery(t, body)
			writeRunPodJSON(t, w, runPodInitialBody(2, 1))
		case 2:
			assertRunPodFollowupQuery(t, body, 1)
			writeRunPodJSON(t, w, runPodFollowupBody(
				`{"gpuTypeId":"SYN-GPU-B","gpuName":"Synthetic GPU B","uninterruptablePrice":9007199254740993.125,"stockStatus":"High","availableGpuCounts":[1],"minVcpu":8,"minMemory":32,"minDisk":64,"countryCode":"US"}`,
				`{"gpuTypeId":"SYN-GPU-B","gpuName":"Synthetic GPU B","uninterruptablePrice":1e-3,"stockStatus":"Low","availableGpuCounts":[1],"minVcpu":4,"minMemory":16,"minDisk":32,"countryCode":"DE"}`,
			))
		case 3:
			assertRunPodFollowupQuery(t, body, 2)
			writeRunPodJSON(t, w, runPodFollowupBody(
				`{"gpuTypeId":"SYN-GPU-B","gpuName":"Synthetic GPU B","uninterruptablePrice":0.25,"stockStatus":"Medium","availableGpuCounts":[2],"minVcpu":8,"minMemory":32,"minDisk":64,"countryCode":"US"}`,
				`null`,
			))
		default:
			t.Errorf("unexpected request %d", requestNumber)
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	adapter := newRunPod(server.URL+"/graphql", runPodTestToken, server.Client(), fixedRunPodClock)
	snapshot, err := adapter.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if calls.Load() != 3 {
		t.Fatalf("requests = %d, want 3", calls.Load())
	}
	assertRunPodCoverage(t, snapshot.Coverage, true, false)
	assertRunPodCredentialWarning(t, snapshot.Warnings)

	wantIDs := []string{
		"SYN-GPU-B:community:1:DE",
		"SYN-GPU-B:secure:1:US",
		"SYN-GPU-B:secure:2:US",
	}
	if len(snapshot.Dispositions) != len(wantIDs) {
		t.Fatalf("dispositions = %#v, want IDs %v", snapshot.Dispositions, wantIDs)
	}
	for index, wantID := range wantIDs {
		disposition := snapshot.Dispositions[index]
		if disposition.SourceID != wantID || disposition.Provider != ProviderRunPod || disposition.Status != DispositionRejected || disposition.Offer != nil {
			t.Fatalf("disposition[%d] = %#v, want rejected %q", index, disposition, wantID)
		}
		assertRunPodReasonCodes(t, disposition.Reasons,
			ReasonBillingIncrementUnknown,
			ReasonMinimumResourcesOnly,
			ReasonPriceScopeUnknown,
			ReasonRegionUnknown,
		)
		if err := disposition.Validate(); err != nil {
			t.Fatalf("disposition[%d] validation: %v", index, err)
		}
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{runPodTestToken, "9007199254740993.125", "0.001"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("snapshot leaked %q: %s", secret, encoded)
		}
	}
}

func TestRunPodEmitsEveryAvailableCountWithStableBoundedIDs(t *testing.T) {
	t.Parallel()
	server := newRunPodSequenceServer(t,
		runPodInitialBody(1, 0),
		runPodFollowupBody(
			`{"gpuTypeId":"SYN-GPU-B","gpuName":"Synthetic GPU B","uninterruptablePrice":0.125,"stockStatus":"High","availableGpuCounts":[17,2,1,2],"minVcpu":8,"minMemory":32,"minDisk":64,"countryCode":"US"}`,
			`null`,
		),
	)
	snapshot, err := newRunPod(server.URL+"/graphql", runPodTestToken, server.Client(), fixedRunPodClock).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wantIDs := []string{
		"SYN-GPU-B:secure:17:US",
		"SYN-GPU-B:secure:1:US",
		"SYN-GPU-B:secure:2:US",
	}
	if len(snapshot.Dispositions) != len(wantIDs) {
		t.Fatalf("dispositions = %#v, want %d", snapshot.Dispositions, len(wantIDs))
	}
	for index, wantID := range wantIDs {
		if snapshot.Dispositions[index].SourceID != wantID {
			t.Fatalf("source IDs = %#v, want %v", runPodDispositionIDs(snapshot.Dispositions), wantIDs)
		}
	}
	if !hasReasonCode(snapshot.Dispositions[0].Reasons, ReasonInvalidValue) {
		t.Fatalf("over-limit reasons = %#v, want invalid_value", snapshot.Dispositions[0].Reasons)
	}
}

func TestRunPodLongTypeIDsRemainDistinctInSyntheticComposites(t *testing.T) {
	t.Parallel()
	typeA := strings.Repeat("é", 80) + "A"
	typeB := strings.Repeat("é", 80) + "B"
	initial := fmt.Sprintf(`{"data":{"gpuTypes":[
		{"id":%s,"displayName":"Synthetic GPU A","memoryInGb":48,"secureCloud":true,"communityCloud":false,"maxGpuCountSecureCloud":1,"maxGpuCountCommunityCloud":0},
		{"id":%s,"displayName":"Synthetic GPU B","memoryInGb":48,"secureCloud":true,"communityCloud":false,"maxGpuCountSecureCloud":1,"maxGpuCountCommunityCloud":0}
	]}}`, strconv.Quote(typeA), strconv.Quote(typeB))
	followup := fmt.Sprintf(`{"data":{"gpuTypes":[
		{"id":%s,"displayName":"Synthetic GPU A","memoryInGb":48,"secureCloud":true,"communityCloud":false,"secureOffer":{"gpuTypeId":%s,"gpuName":"Synthetic GPU A","uninterruptablePrice":0.1,"stockStatus":"High","availableGpuCounts":[1],"minVcpu":4,"minMemory":16,"minDisk":32,"countryCode":"US"}},
		{"id":%s,"displayName":"Synthetic GPU B","memoryInGb":48,"secureCloud":true,"communityCloud":false,"secureOffer":{"gpuTypeId":%s,"gpuName":"Synthetic GPU B","uninterruptablePrice":0.2,"stockStatus":"High","availableGpuCounts":[1],"minVcpu":4,"minMemory":16,"minDisk":32,"countryCode":"US"}}
	]}}`, strconv.Quote(typeA), strconv.Quote(typeA), strconv.Quote(typeB), strconv.Quote(typeB))
	server := newRunPodSequenceServer(t, initial, followup)
	snapshot, err := newRunPod(server.URL+"/graphql", runPodTestToken, server.Client(), fixedRunPodClock).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Dispositions) != 2 || snapshot.Dispositions[0].SourceID == snapshot.Dispositions[1].SourceID {
		t.Fatalf("source IDs = %v, want two distinct composites", runPodDispositionIDs(snapshot.Dispositions))
	}
	for _, disposition := range snapshot.Dispositions {
		if !strings.HasSuffix(disposition.SourceID, ":secure:1:US") || disposition.Validate() != nil {
			t.Fatalf("disposition = %#v", disposition)
		}
	}
}

func TestRunPodUnexpectedFollowupTypesRemainDistinctAcrossCounts(t *testing.T) {
	t.Parallel()
	initial := `{"data":{"gpuTypes":[
		{"id":"SYN-GPU-A","displayName":"Synthetic GPU A","memoryInGb":48,"secureCloud":true,"communityCloud":false,"maxGpuCountSecureCloud":2,"maxGpuCountCommunityCloud":0}
	]}}`
	followup := func(unexpectedID string) string {
		return fmt.Sprintf(`{"data":{"gpuTypes":[
			{"id":"SYN-GPU-A","displayName":"Synthetic GPU A","memoryInGb":48,"secureCloud":true,"communityCloud":false,"secureOffer":null},
			{"id":%s,"displayName":"Unexpected Synthetic GPU","memoryInGb":48,"secureCloud":true,"communityCloud":false,"secureOffer":{"gpuTypeId":%s,"gpuName":"Unexpected Synthetic GPU","uninterruptablePrice":0.125,"stockStatus":"High","availableGpuCounts":[1],"minVcpu":8,"minMemory":32,"minDisk":64,"countryCode":"US"},"communityOffer":null}
		]}}`, strconv.Quote(unexpectedID), strconv.Quote(unexpectedID))
	}
	server := newRunPodSequenceServer(t, initial, followup("SYN-GPU-X"), followup("SYN-GPU-Y"))
	snapshot, err := newRunPod(server.URL+"/graphql", runPodTestToken, server.Client(), fixedRunPodClock).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	assertRunPodCoverage(t, snapshot.Coverage, true, false)
	assertRunPodCredentialWarning(t, snapshot.Warnings)

	wantIDs := []string{
		"SYN-GPU-A:secure:1:unknown",
		"SYN-GPU-A:secure:2:unknown",
		"SYN-GPU-X:community:1:unknown",
		"SYN-GPU-X:secure:1:US",
		"SYN-GPU-Y:community:2:unknown",
		"SYN-GPU-Y:secure:1:US",
		"SYN-GPU-Y:secure:2:US",
	}
	if got := runPodDispositionIDs(snapshot.Dispositions); fmt.Sprint(got) != fmt.Sprint(wantIDs) {
		t.Fatalf("source IDs = %v, want %v", got, wantIDs)
	}
	for _, disposition := range snapshot.Dispositions {
		if disposition.Status != DispositionRejected || disposition.Offer != nil {
			t.Fatalf("disposition = %#v, want rejection", disposition)
		}
		if strings.Contains(disposition.SourceID, "SYN-GPU-X") || strings.Contains(disposition.SourceID, "SYN-GPU-Y") {
			if !hasReasonCode(disposition.Reasons, ReasonInvalidValue) {
				t.Fatalf("unexpected type disposition = %#v, want mismatch reason", disposition)
			}
		}
		if err := disposition.Validate(); err != nil {
			t.Fatalf("disposition validation: %v", err)
		}
	}
}

func TestRunPodInvalidFollowupIDsDoNotCollideWithRealIDs(t *testing.T) {
	t.Parallel()
	initial := `{"data":{"gpuTypes":[
		{"id":"item-2","displayName":"Synthetic GPU A","memoryInGb":48,"secureCloud":true,"communityCloud":false,"maxGpuCountSecureCloud":2,"maxGpuCountCommunityCloud":0}
	]}}`
	followup := func(idField string) string {
		return fmt.Sprintf(`{"data":{"gpuTypes":[
			{"id":"item-2","displayName":"Synthetic GPU A","memoryInGb":48,"secureCloud":true,"communityCloud":false,"secureOffer":{"gpuTypeId":"item-2","gpuName":"Synthetic GPU A","uninterruptablePrice":0.125,"stockStatus":"High","availableGpuCounts":[1],"minVcpu":8,"minMemory":32,"minDisk":64,"countryCode":"US"}},
			{%s"displayName":"Invalid ID Synthetic GPU","memoryInGb":48,"secureCloud":true,"communityCloud":false,"secureOffer":{"gpuTypeId":"item-2","gpuName":"Invalid ID Synthetic GPU","uninterruptablePrice":0.125,"stockStatus":"High","availableGpuCounts":[1],"minVcpu":8,"minMemory":32,"minDisk":64,"countryCode":"US"},"communityOffer":null}
		]}}`, idField)
	}
	server := newRunPodSequenceServer(t, initial, followup(""), followup(`"id":"\u0000",`))
	snapshot, err := newRunPod(server.URL+"/graphql", runPodTestToken, server.Client(), fixedRunPodClock).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	assertRunPodCoverage(t, snapshot.Coverage, true, false)

	wantIDs := []string{
		"%00followup-1-item-2:community:1:unknown",
		"%00followup-1-item-2:secure:1:US",
		"%00followup-2-item-2:community:2:unknown",
		"%00followup-2-item-2:secure:1:US",
		"%00followup-2-item-2:secure:2:US",
		"item-2:secure:1:US",
		"item-2:secure:2:US",
	}
	if got := runPodDispositionIDs(snapshot.Dispositions); fmt.Sprint(got) != fmt.Sprint(wantIDs) {
		t.Fatalf("source IDs = %v, want %v", got, wantIDs)
	}
	for _, disposition := range snapshot.Dispositions {
		if err := disposition.Validate(); err != nil {
			t.Fatalf("disposition validation: %v", err)
		}
	}
}

func TestRunPodInvalidFollowupIsAtomic(t *testing.T) {
	t.Parallel()
	initial := `{"data":{"gpuTypes":[
		{"id":"SYN-GPU-A","displayName":"Synthetic GPU A","memoryInGb":24,"secureCloud":true,"communityCloud":false,"maxGpuCountSecureCloud":1,"maxGpuCountCommunityCloud":0},
		{"id":"SYN-GPU-B","displayName":"Synthetic GPU B","memoryInGb":48,"secureCloud":true,"communityCloud":false,"maxGpuCountSecureCloud":1,"maxGpuCountCommunityCloud":0}
	]}}`
	followup := `{"data":{"gpuTypes":[
		{"id":"SYN-GPU-A","displayName":"Synthetic GPU A","memoryInGb":24,"secureCloud":true,"communityCloud":false,"secureOffer":{"gpuTypeId":"SYN-GPU-A","gpuName":"Synthetic GPU A","uninterruptablePrice":0.1,"stockStatus":"High","availableGpuCounts":[1],"minVcpu":4,"minMemory":16,"minDisk":32,"countryCode":"US"}},
		null
	]}}`
	server := newRunPodSequenceServer(t, initial, followup)
	snapshot, err := newRunPod(server.URL+"/graphql", runPodTestToken, server.Client(), fixedRunPodClock).Discover(context.Background())
	assertRunPodFailure(t, snapshot, err, FailureInvalidResponse, 0, 0)
	if len(snapshot.Dispositions) != 0 || len(snapshot.Warnings) != 0 {
		t.Fatalf("invalid follow-up retained partial response state: %#v", snapshot)
	}
}

func TestRunPodFollowupCannotOmitCatalogType(t *testing.T) {
	t.Parallel()
	server := newRunPodSequenceServer(t, runPodInitialBody(1, 0), `{"data":{"gpuTypes":[]}}`)
	snapshot, err := newRunPod(server.URL+"/graphql", runPodTestToken, server.Client(), fixedRunPodClock).Discover(context.Background())
	assertRunPodFailure(t, snapshot, err, FailureInvalidResponse, 0, 0)
	if len(snapshot.Dispositions) != 0 || snapshot.Coverage.Complete {
		t.Fatalf("omitted catalog type produced completed state: %#v", snapshot)
	}
}

func TestRunPodCandidateValidationReasons(t *testing.T) {
	t.Parallel()
	base := `{"gpuTypeId":"SYN-GPU-B","gpuName":"Synthetic GPU B","uninterruptablePrice":0.125,"stockStatus":"High","availableGpuCounts":[1],"minVcpu":8,"minMemory":32,"minDisk":64,"countryCode":"US"}`
	tests := []struct {
		name       string
		initial    string
		secure     string
		wantReason ReasonCode
	}{
		{name: "high", initial: runPodInitialBody(1, 0), secure: base},
		{name: "medium", initial: runPodInitialBody(1, 0), secure: strings.Replace(base, `"High"`, `"Medium"`, 1)},
		{name: "low", initial: runPodInitialBody(1, 0), secure: strings.Replace(base, `"High"`, `"Low"`, 1)},
		{name: "none", initial: runPodInitialBody(1, 0), secure: strings.Replace(base, `"High"`, `"None"`, 1), wantReason: ReasonUnavailable},
		{name: "unknown stock", initial: runPodInitialBody(1, 0), secure: strings.Replace(base, `"High"`, `"Synthetic"`, 1), wantReason: ReasonInvalidValue},
		{name: "missing price", initial: runPodInitialBody(1, 0), secure: strings.Replace(base, `"uninterruptablePrice":0.125,`, "", 1), wantReason: ReasonPriceMissing},
		{name: "null price", initial: runPodInitialBody(1, 0), secure: strings.Replace(base, `0.125`, `null`, 1), wantReason: ReasonPriceMissing},
		{name: "invalid price", initial: runPodInitialBody(1, 0), secure: strings.Replace(base, `0.125`, `0`, 1), wantReason: ReasonInvalidValue},
		{name: "missing requested count", initial: runPodInitialBody(1, 0), secure: strings.Replace(base, `[1]`, `[]`, 1), wantReason: ReasonMissingRequired},
		{name: "mismatched type", initial: runPodInitialBody(1, 0), secure: strings.Replace(base, `"SYN-GPU-B"`, `"SYN-GPU-X"`, 1), wantReason: ReasonInvalidValue},
		{name: "mismatched GPU name", initial: runPodInitialBody(1, 0), secure: strings.Replace(base, `"gpuName":"Synthetic GPU B"`, `"gpuName":"Synthetic GPU X"`, 1), wantReason: ReasonInvalidValue},
		{name: "invalid memory", initial: strings.Replace(runPodInitialBody(1, 0), `"memoryInGb":48`, `"memoryInGb":0`, 1), secure: base, wantReason: ReasonInvalidValue},
		{name: "cloud support false", initial: strings.Replace(runPodInitialBody(1, 0), `"secureCloud":true`, `"secureCloud":false`, 1), secure: base, wantReason: ReasonUnavailable},
		{name: "absent stock", initial: runPodInitialBody(1, 0), secure: `null`, wantReason: ReasonMissingRequired},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := newRunPodSequenceServer(t, test.initial, runPodFollowupBody(test.secure, `null`))
			snapshot, err := newRunPod(server.URL+"/graphql", runPodTestToken, server.Client(), fixedRunPodClock).Discover(context.Background())
			if err != nil {
				t.Fatalf("Discover() error = %v", err)
			}
			if len(snapshot.Dispositions) != 1 {
				t.Fatalf("dispositions = %#v, want one", snapshot.Dispositions)
			}
			reasons := snapshot.Dispositions[0].Reasons
			assertRunPodReasonCodes(t, reasons, ReasonBillingIncrementUnknown, ReasonMinimumResourcesOnly, ReasonPriceScopeUnknown, ReasonRegionUnknown)
			if test.wantReason != "" && !hasReasonCode(reasons, test.wantReason) {
				t.Fatalf("reasons = %#v, want %q", reasons, test.wantReason)
			}
			if test.wantReason == "" && (hasReasonCode(reasons, ReasonUnavailable) || hasReasonCode(reasons, ReasonInvalidValue) || hasReasonCode(reasons, ReasonPriceMissing)) {
				t.Fatalf("candidate reasons = %#v, want no availability/value/price rejection", reasons)
			}
		})
	}
}

func TestRunPodGraphQLErrorsAndInvalidFramingFailWholeResponse(t *testing.T) {
	t.Parallel()
	tests := []string{
		`{"errors":[{"message":"synthetic-private-graphql-error"}],"data":{"gpuTypes":[]}}`,
		`{"data":`,
		`{}`,
		`{"data":null}`,
		`{"data":{"gpuTypes":null}}`,
		`{"data":{"gpuTypes":{}}}`,
		`{"data":{"gpuTypes":[]}} {"data":{"gpuTypes":[]}}`,
		strings.Replace(runPodInitialBody(1, 0), `"maxGpuCountSecureCloud":1,`, "", 1),
		strings.Replace(runPodInitialBody(1, 0), `"maxGpuCountSecureCloud":1`, `"maxGpuCountSecureCloud":null`, 1),
		strings.Replace(runPodInitialBody(1, 0), `"maxGpuCountSecureCloud":1`, `"maxGpuCountSecureCloud":"1"`, 1),
	}
	for index, body := range tests {
		t.Run(strconv.Itoa(index), func(t *testing.T) {
			t.Parallel()
			server := newRunPodSequenceServer(t, body)
			snapshot, err := newRunPod(server.URL+"/graphql", runPodTestToken, server.Client(), fixedRunPodClock).Discover(context.Background())
			assertRunPodFailure(t, snapshot, err, FailureInvalidResponse, 0, 0)
			if strings.Contains(err.Error(), "synthetic-private") {
				t.Fatalf("error leaked GraphQL body: %v", err)
			}
		})
	}
}

func TestRunPodHTTPFailuresAreTypedSafeAndNotRetried(t *testing.T) {
	t.Parallel()
	tests := []struct {
		status     int
		code       FailureCode
		retryAfter int64
	}{
		{status: 400, code: FailureRequest},
		{status: 401, code: FailureAuthentication},
		{status: 403, code: FailureAuthentication},
		{status: 429, code: FailureRateLimited, retryAfter: 41},
		{status: 500, code: FailureProviderUnavailable},
	}
	for _, test := range tests {
		t.Run(strconv.Itoa(test.status), func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			server := newRunPodTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				body := readRunPodRequest(t, r)
				assertRunPodRequestSafety(t, r, body)
				w.Header().Set("Content-Type", "text/plain")
				if test.retryAfter != 0 {
					w.Header().Set("Retry-After", strconv.FormatInt(test.retryAfter, 10))
				}
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, "synthetic-private-runpod-body")
			})
			snapshot, err := newRunPod(server.URL+"/graphql", runPodTestToken, server.Client(), fixedRunPodClock).Discover(context.Background())
			assertRunPodFailure(t, snapshot, err, test.code, test.status, test.retryAfter)
			if calls.Load() != 1 || strings.Contains(err.Error(), "synthetic-private") || strings.Contains(err.Error(), runPodTestToken) {
				t.Fatalf("calls=%d error=%v", calls.Load(), err)
			}
		})
	}
}

func TestRunPodMissingCredentialMakesNoRequest(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	server := newRunPodTLSServer(t, func(http.ResponseWriter, *http.Request) { calls.Add(1) })
	snapshot, err := newRunPod(server.URL+"/graphql", " ", server.Client(), fixedRunPodClock).Discover(context.Background())
	assertRunPodFailure(t, snapshot, err, FailureNotConfigured, 0, 0)
	if calls.Load() != 0 {
		t.Fatalf("requests = %d, want zero", calls.Load())
	}
}

func TestRunPodFollowupFailurePreservesPartialDiscovery(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	server := newRunPodTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
		requestNumber := int(calls.Add(1))
		body := readRunPodRequest(t, r)
		assertRunPodRequestSafety(t, r, body)
		switch requestNumber {
		case 1:
			writeRunPodJSON(t, w, runPodInitialBody(3, 0))
		case 2:
			assertRunPodFollowupQuery(t, body, 1)
			writeRunPodJSON(t, w, runPodFollowupBody(
				`{"gpuTypeId":"SYN-GPU-B","gpuName":"Synthetic GPU B","uninterruptablePrice":0.125,"stockStatus":"High","availableGpuCounts":[1],"minVcpu":8,"minMemory":32,"minDisk":64,"countryCode":"US"}`,
				`null`,
			))
		case 3:
			assertRunPodFollowupQuery(t, body, 2)
			w.Header().Set("Retry-After", "23")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, "synthetic-private-followup-body")
		default:
			t.Errorf("unexpected request %d", requestNumber)
		}
	})
	adapter := newRunPod(server.URL+"/graphql", runPodTestToken, server.Client(), fixedRunPodClock)
	snapshot, err := adapter.Discover(context.Background())
	assertRunPodFailure(t, snapshot, err, FailureRateLimited, http.StatusTooManyRequests, 23)
	if len(snapshot.Dispositions) != 1 || snapshot.Dispositions[0].SourceID != "SYN-GPU-B:secure:1:US" || calls.Load() != 3 {
		t.Fatalf("snapshot=%#v calls=%d", snapshot, calls.Load())
	}
	assertRunPodCredentialWarning(t, snapshot.Warnings)
	if snapshot.Coverage.Complete || snapshot.Coverage.Truncated {
		t.Fatalf("partial coverage = %#v", snapshot.Coverage)
	}
	discoverer, createErr := NewDiscoverer(adapter)
	if createErr != nil {
		t.Fatal(createErr)
	}
	// Use a fresh server-backed adapter because discovery is not retried internally.
	_ = discoverer
	secondCalls := atomic.Int32{}
	partialServer := newRunPodTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
		requestNumber := int(secondCalls.Add(1))
		_ = readRunPodRequest(t, r)
		switch requestNumber {
		case 1:
			writeRunPodJSON(t, w, runPodInitialBody(2, 0))
		case 2:
			writeRunPodJSON(t, w, runPodFollowupBody(
				`{"gpuTypeId":"SYN-GPU-B","gpuName":"Synthetic GPU B","uninterruptablePrice":0.125,"stockStatus":"High","availableGpuCounts":[1],"minVcpu":8,"minMemory":32,"minDisk":64,"countryCode":"US"}`,
				`null`,
			))
		case 3:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
	partialAdapter := newRunPod(partialServer.URL+"/graphql", runPodTestToken, partialServer.Client(), fixedRunPodClock)
	partialDiscoverer, createErr := NewDiscoverer(partialAdapter)
	if createErr != nil {
		t.Fatal(createErr)
	}
	results := partialDiscoverer.Discover(context.Background())
	if len(results) != 1 || results[0].Status != ResultPartial || results[0].Failure == nil || results[0].Failure.Code != FailureProviderUnavailable || len(results[0].Dispositions) != 1 {
		t.Fatalf("results = %#v", results)
	}
}

func TestRunPodFirstFollowupFailureHasFailedSemantics(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	server := newRunPodTLSServer(t, func(w http.ResponseWriter, _ *http.Request) {
		switch calls.Add(1) {
		case 1:
			writeRunPodJSON(t, w, runPodInitialBody(1, 0))
		case 2:
			w.WriteHeader(http.StatusInternalServerError)
		default:
			t.Error("unexpected retry")
		}
	})
	discoverer, err := NewDiscoverer(newRunPod(server.URL+"/graphql", runPodTestToken, server.Client(), fixedRunPodClock))
	if err != nil {
		t.Fatal(err)
	}
	result := discoverer.Discover(context.Background())[0]
	if result.Status != ResultFailed || result.Failure == nil || result.Failure.Code != FailureProviderUnavailable || len(result.Dispositions) != 0 || calls.Load() != 2 {
		t.Fatalf("result=%#v calls=%d", result, calls.Load())
	}
}

func TestRunPodAdvertisedMaximumIsCappedAtSixteen(t *testing.T) {
	t.Parallel()
	var counts []int
	server := newRunPodTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
		body := readRunPodRequest(t, r)
		if len(counts) == 0 {
			counts = append(counts, 0)
			writeRunPodJSON(t, w, runPodInitialBody(17, 0))
			return
		}
		var request struct {
			Variables map[string]int `json:"variables"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			t.Error(err)
		}
		counts = append(counts, request.Variables["gpuCount"])
		writeRunPodJSON(t, w, runPodFollowupBody(`null`, `null`))
	})
	snapshot, err := newRunPod(server.URL+"/graphql", runPodTestToken, server.Client(), fixedRunPodClock).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := make([]int, 17)
	for count := 1; count <= 16; count++ {
		want[count] = count
	}
	if fmt.Sprint(counts) != fmt.Sprint(want) {
		t.Fatalf("query counts = %v, want %v", counts, want)
	}
	assertRunPodCoverage(t, snapshot.Coverage, false, true)
	if !hasReasonCode(snapshot.Coverage.Notes, ReasonResultsTruncated) {
		t.Fatalf("coverage notes = %#v", snapshot.Coverage.Notes)
	}
	assertRunPodCredentialWarning(t, snapshot.Warnings)
	discoverer, createErr := NewDiscoverer(newRunPodSequenceAdapter(t, runPodInitialBody(17, 0), 16))
	if createErr != nil {
		t.Fatal(createErr)
	}
	result := discoverer.Discover(context.Background())[0]
	if result.Status != ResultPartial || result.Failure != nil || !result.Coverage.Truncated {
		t.Fatalf("result = %#v", result)
	}
}

func TestRunPodProductionEndpointCoverageAndNilDependencies(t *testing.T) {
	t.Parallel()
	var gotURL string
	client := &http.Client{Transport: runPodRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		gotURL = request.URL.String()
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(runPodInitialBody(0, 0))),
		}, nil
	})}
	adapter := NewRunPod(runPodTestToken, client, nil)
	coverage := adapter.Coverage()
	assertRunPodCoverage(t, coverage, false, false)
	coverage.Filters[0] = "mutated"
	if adapter.Coverage().Filters[0] == "mutated" {
		t.Fatal("Coverage() returned shared filters")
	}
	snapshot, err := adapter.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotURL != "https://api.runpod.io/graphql" || !snapshot.Coverage.Complete {
		t.Fatalf("url=%q coverage=%#v", gotURL, snapshot.Coverage)
	}

	var requests atomic.Int32
	noRequestClient := &http.Client{Transport: runPodRoundTripFunc(func(*http.Request) (*http.Response, error) {
		requests.Add(1)
		return nil, errors.New("unexpected request")
	})}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	snapshot, err = newRunPod("https://catalog.example.test/graphql", runPodTestToken, noRequestClient, nil).Discover(canceled)
	assertRunPodFailure(t, snapshot, err, FailureRequest, 0, 0)
	if requests.Load() != 0 {
		t.Fatalf("canceled context requests = %d", requests.Load())
	}
	snapshot, err = newRunPod("https://catalog.example.test/graphql", runPodTestToken, noRequestClient, nil).Discover(nil)
	assertRunPodFailure(t, snapshot, err, FailureRequest, 0, 0)
	if requests.Load() != 0 {
		t.Fatalf("nil context requests = %d", requests.Load())
	}
}

type runPodRoundTripFunc func(*http.Request) (*http.Response, error)

func (f runPodRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func newRunPodSequenceAdapter(t *testing.T, initial string, followups int) Adapter {
	t.Helper()
	responses := make([]string, 1, followups+1)
	responses[0] = initial
	for range followups {
		responses = append(responses, runPodFollowupBody(`null`, `null`))
	}
	server := newRunPodSequenceServer(t, responses...)
	return newRunPod(server.URL+"/graphql", runPodTestToken, server.Client(), fixedRunPodClock)
}

func newRunPodSequenceServer(t *testing.T, responses ...string) *httptest.Server {
	t.Helper()
	var calls atomic.Int32
	return newRunPodTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
		call := int(calls.Add(1))
		_ = readRunPodRequest(t, r)
		if call > len(responses) {
			t.Errorf("unexpected request %d", call)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		writeRunPodJSON(t, w, responses[call-1])
	})
}

func newRunPodTLSServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	return server
}

func readRunPodRequest(t *testing.T, request *http.Request) []byte {
	t.Helper()
	body, err := io.ReadAll(request.Body)
	if err != nil {
		t.Error(err)
	}
	return body
}

func assertRunPodRequestSafety(t *testing.T, request *http.Request, body []byte) {
	t.Helper()
	if request.Method != http.MethodPost || request.URL.Path != "/graphql" || request.URL.RawQuery != "" {
		t.Errorf("request = %s %s", request.Method, request.URL.String())
	}
	if request.Header.Get("Authorization") != "Bearer "+runPodTestToken || request.Header.Get("Accept") != "application/json" || request.Header.Get("Content-Type") != "application/json" {
		t.Errorf("headers = %#v", request.Header)
	}
	if strings.Contains(string(body), runPodTestToken) || strings.Contains(strings.ToLower(string(body)), "mutation") {
		t.Errorf("unsafe GraphQL body = %s", body)
	}
}

func assertRunPodInitialQuery(t *testing.T, body []byte) {
	t.Helper()
	var request struct {
		Query     string         `json:"query"`
		Variables map[string]int `json:"variables"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		t.Errorf("request JSON: %v", err)
		return
	}
	if len(request.Variables) != 0 || strings.Contains(request.Query, "lowestPrice") {
		t.Errorf("initial request = %#v", request)
	}
	for _, field := range []string{"query", "gpuTypes", "id", "displayName", "memoryInGb", "secureCloud", "communityCloud", "maxGpuCountSecureCloud", "maxGpuCountCommunityCloud"} {
		if !strings.Contains(request.Query, field) {
			t.Errorf("initial query missing %q: %s", field, request.Query)
		}
	}
}

func assertRunPodFollowupQuery(t *testing.T, body []byte, count int) {
	t.Helper()
	var request struct {
		Query     string         `json:"query"`
		Variables map[string]int `json:"variables"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		t.Errorf("request JSON: %v", err)
		return
	}
	if request.Variables["gpuCount"] != count || len(request.Variables) != 1 {
		t.Errorf("variables = %#v, want gpuCount=%d", request.Variables, count)
	}
	for _, field := range []string{
		"query", "$gpuCount", "gpuTypes", "id", "displayName", "memoryInGb", "secureCloud", "communityCloud",
		"secureOffer: lowestPrice", "communityOffer: lowestPrice", "gpuCount: $gpuCount", "secureCloud: true", "secureCloud: false",
		"gpuTypeId", "gpuName", "uninterruptablePrice", "stockStatus", "availableGpuCounts", "minVcpu", "minMemory", "minDisk", "countryCode",
	} {
		if !strings.Contains(request.Query, field) {
			t.Errorf("follow-up query missing %q: %s", field, request.Query)
		}
	}
}

func writeRunPodJSON(t *testing.T, writer http.ResponseWriter, body string) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	if _, err := io.WriteString(writer, body); err != nil {
		t.Error(err)
	}
}

func runPodInitialBody(maxSecure, maxCommunity int) string {
	return fmt.Sprintf(`{"data":{"gpuTypes":[{"id":"SYN-GPU-B","displayName":"Synthetic GPU B","memoryInGb":48,"secureCloud":true,"communityCloud":%t,"maxGpuCountSecureCloud":%d,"maxGpuCountCommunityCloud":%d}]}}`, maxCommunity > 0, maxSecure, maxCommunity)
}

func runPodFollowupBody(secure, community string) string {
	return fmt.Sprintf(`{"data":{"gpuTypes":[{"id":"SYN-GPU-B","displayName":"Synthetic GPU B","memoryInGb":48,"secureCloud":true,"communityCloud":true,"secureOffer":%s,"communityOffer":%s}]}}`, secure, community)
}

func fixedRunPodClock() time.Time {
	return time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC)
}

func assertRunPodFailure(t *testing.T, snapshot Snapshot, err error, code FailureCode, status int, retryAfter int64) {
	t.Helper()
	var adapterErr *AdapterError
	if !errors.As(err, &adapterErr) {
		t.Fatalf("error = %v, want AdapterError", err)
	}
	if adapterErr.Provider != ProviderRunPod || adapterErr.Code != code || adapterErr.HTTPStatus != status || adapterErr.RetryAfterSeconds != retryAfter {
		t.Fatalf("adapter error = %#v", adapterErr)
	}
	if snapshot.Coverage.Complete || snapshot.Coverage.Truncated || snapshot.Coverage.Validate() != nil {
		t.Fatalf("failure coverage = %#v", snapshot.Coverage)
	}
}

func assertRunPodCoverage(t *testing.T, coverage Coverage, complete, truncated bool) {
	t.Helper()
	if coverage.Market != "secure_and_community_lowest_price" || fmt.Sprint(coverage.Filters) != "[cloud=community cloud=secure]" || coverage.Limit != 16 || coverage.Complete != complete || coverage.Truncated != truncated {
		t.Fatalf("coverage = %#v", coverage)
	}
	if err := coverage.Validate(); err != nil {
		t.Fatalf("coverage validation: %v", err)
	}
}

func assertRunPodCredentialWarning(t *testing.T, warnings []Reason) {
	t.Helper()
	if len(warnings) != 1 || warnings[0].Code != ReasonCredentialScopeUnverified || warnings[0].Field != "credentials" || warnings[0].validate() != nil {
		t.Fatalf("warnings = %#v", warnings)
	}
}

func assertRunPodReasonCodes(t *testing.T, reasons []Reason, codes ...ReasonCode) {
	t.Helper()
	for _, code := range codes {
		if !hasReasonCode(reasons, code) {
			t.Errorf("reasons = %#v, missing %q", reasons, code)
		}
	}
}

func runPodDispositionIDs(dispositions []Disposition) []string {
	ids := make([]string, len(dispositions))
	for index, disposition := range dispositions {
		ids[index] = disposition.SourceID
	}
	return ids
}
