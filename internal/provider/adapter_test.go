package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

type discovererFakeAdapter struct {
	provider      Provider
	coverage      Coverage
	providerCalls int
	coverageCalls int
	discover      func(context.Context) (Snapshot, error)
}

func (f *discovererFakeAdapter) Provider() Provider {
	f.providerCalls++
	return f.provider
}

func (f *discovererFakeAdapter) Coverage() Coverage {
	f.coverageCalls++
	return f.coverage
}

func (f *discovererFakeAdapter) Discover(ctx context.Context) (Snapshot, error) {
	if f.discover != nil {
		return f.discover(ctx)
	}
	return Snapshot{}, nil
}

func discovererTestCoverage() Coverage {
	return Coverage{
		Market:  "on-demand",
		Filters: []string{"gpu=true", "rentable=true"},
		Limit:   100,
		Notes: []Reason{{
			Code:    ReasonCoverageLimited,
			Message: "synthetic coverage note",
		}},
	}
}

func discovererActualCoverage() Coverage {
	coverage := discovererTestCoverage()
	coverage.Complete = true
	return coverage
}

func TestAdapterErrorHasStableTypedOutput(t *testing.T) {
	adapterErr := AdapterError{
		Provider:          ProviderRunPod,
		Code:              FailureRateLimited,
		HTTPStatus:        429,
		RetryAfterSeconds: 60,
	}

	if err := adapterErr.validate(); err != nil {
		t.Fatalf("AdapterError.validate() error = %v, want nil", err)
	}
	if err := adapterErr.validateTransportError(); err != nil {
		t.Fatalf("AdapterError.validateTransportError() error = %v, want nil", err)
	}
	if got, want := adapterErr.Error(), "provider runpod request failed: rate_limited (http_status=429, retry_after_seconds=60)"; got != want {
		t.Errorf("AdapterError.Error() = %q, want %q", got, want)
	}

	encoded, err := json.Marshal(adapterErr)
	if err != nil {
		t.Fatalf("json.Marshal(AdapterError) error = %v", err)
	}
	if got, want := string(encoded), `{"provider":"runpod","code":"rate_limited","http_status":429,"retry_after_seconds":60}`; got != want {
		t.Errorf("json.Marshal(AdapterError) = %s, want %s", got, want)
	}
}

func TestAdapterErrorValidatesTypedFields(t *testing.T) {
	validCodes := []FailureCode{
		FailureNotConfigured,
		FailureAuthentication,
		FailureRateLimited,
		FailureProviderUnavailable,
		FailureTimeout,
		FailureInvalidResponse,
		FailureRequest,
	}
	for _, code := range validCodes {
		adapterErr := AdapterError{Provider: ProviderLambda, Code: code}
		if err := adapterErr.validate(); err != nil {
			t.Errorf("AdapterError.validate() with code %q error = %v, want nil", code, err)
		}
	}

	tests := []struct {
		name       string
		adapterErr AdapterError
	}{
		{name: "provider", adapterErr: AdapterError{Provider: "synthetic-private-provider", Code: FailureRequest}},
		{name: "code", adapterErr: AdapterError{Provider: ProviderVastAI, Code: "synthetic-private-code"}},
		{name: "status below range", adapterErr: AdapterError{Provider: ProviderVastAI, Code: FailureRequest, HTTPStatus: 99}},
		{name: "success status", adapterErr: AdapterError{Provider: ProviderVastAI, Code: FailureRequest, HTTPStatus: 204}},
		{name: "status above range", adapterErr: AdapterError{Provider: ProviderVastAI, Code: FailureRequest, HTTPStatus: 600}},
		{name: "negative retry", adapterErr: AdapterError{Provider: ProviderVastAI, Code: FailureRequest, RetryAfterSeconds: -1}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.adapterErr.validate(); err == nil {
				t.Fatal("AdapterError.validate() error = nil, want non-nil")
			}
			for _, secret := range []string{"synthetic-private-provider", "synthetic-private-code"} {
				if strings.Contains(test.adapterErr.Error(), secret) {
					t.Fatalf("AdapterError.Error() leaked rejected field %q", secret)
				}
			}
		})
	}
}

func TestAdapterErrorSeparatesStrictAndTransportValidation(t *testing.T) {
	sentinel := AdapterError{Code: FailureRequest}
	if err := sentinel.validate(); err == nil {
		t.Fatal("sentinel AdapterError.validate() error = nil, want non-nil")
	}
	if err := sentinel.validateTransportError(); err != nil {
		t.Fatalf("sentinel AdapterError.validateTransportError() error = %v, want nil", err)
	}
	if got, want := sentinel.Error(), "provider request failed: request_failed"; got != want {
		t.Errorf("sentinel AdapterError.Error() = %q, want %q", got, want)
	}
	encoded, err := json.Marshal(sentinel)
	if err == nil {
		t.Fatalf("json.Marshal(sentinel AdapterError) = %s, nil; want error", encoded)
	}
	if len(encoded) != 0 {
		t.Errorf("json.Marshal(sentinel AdapterError) output = %q, want empty", encoded)
	}

	invalid := []AdapterError{
		{Code: FailureTimeout},
		{Code: FailureRequest, HTTPStatus: 400},
		{Code: FailureRequest, RetryAfterSeconds: 1},
	}
	for _, adapterErr := range invalid {
		if err := adapterErr.validate(); err == nil {
			t.Errorf("AdapterError.validate() for %#v error = nil, want non-nil", adapterErr)
		}
		if err := adapterErr.validateTransportError(); err == nil {
			t.Errorf("AdapterError.validateTransportError() for %#v error = nil, want non-nil", adapterErr)
		}
	}
}

func TestAdapterErrorMarshalJSONRejectsInvalidFieldsWithoutLeaking(t *testing.T) {
	tests := []struct {
		name       string
		adapterErr AdapterError
	}{
		{name: "provider", adapterErr: AdapterError{Provider: "synthetic-private-provider", Code: FailureRequest}},
		{name: "code", adapterErr: AdapterError{Provider: ProviderRunPod, Code: "synthetic-private-code"}},
		{name: "status", adapterErr: AdapterError{Provider: ProviderRunPod, Code: FailureRequest, HTTPStatus: 600}},
		{name: "retry", adapterErr: AdapterError{Provider: ProviderRunPod, Code: FailureRequest, RetryAfterSeconds: -1}},
		{name: "inexact sentinel", adapterErr: AdapterError{Code: FailureTimeout}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := json.Marshal(test.adapterErr)
			if err == nil {
				t.Fatalf("json.Marshal(AdapterError) = %s, nil; want error", encoded)
			}
			if len(encoded) != 0 {
				t.Errorf("json.Marshal(AdapterError) output = %q, want empty", encoded)
			}
			for _, secret := range []string{"synthetic-private-provider", "synthetic-private-code"} {
				if strings.Contains(err.Error(), secret) || strings.Contains(string(encoded), secret) {
					t.Fatalf("json.Marshal(AdapterError) leaked %q", secret)
				}
			}
		})
	}
}

func TestAdapterErrorFactorySanitizesInvalidTypedFields(t *testing.T) {
	tests := []struct {
		name        string
		got         *AdapterError
		want        AdapterError
		strictValid bool
	}{
		{
			name: "provider",
			got:  newAdapterError("synthetic-private-provider", FailureAuthentication, 401, 10),
			want: AdapterError{Code: FailureRequest},
		},
		{
			name:        "code",
			got:         newAdapterError(ProviderRunPod, "synthetic-private-code", 400, 10),
			want:        AdapterError{Provider: ProviderRunPod, Code: FailureRequest},
			strictValid: true,
		},
		{
			name:        "status",
			got:         newAdapterError(ProviderLambda, FailureRequest, 600, 10),
			want:        AdapterError{Provider: ProviderLambda, Code: FailureRequest},
			strictValid: true,
		},
		{
			name:        "retry",
			got:         newAdapterError(ProviderVastAI, FailureRateLimited, 429, -1),
			want:        AdapterError{Provider: ProviderVastAI, Code: FailureRequest},
			strictValid: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.got == nil || *test.got != test.want {
				t.Fatalf("newAdapterError() = %#v, want %#v", test.got, test.want)
			}
			if err := test.got.validateTransportError(); err != nil {
				t.Errorf("newAdapterError().validateTransportError() error = %v, want nil", err)
			}
			strictErr := test.got.validate()
			if test.strictValid && strictErr != nil {
				t.Errorf("newAdapterError().validate() error = %v, want nil", strictErr)
			}
			if !test.strictValid && strictErr == nil {
				t.Error("newAdapterError().validate() error = nil, want non-nil")
			}
		})
	}
}

func TestDiscovererRejectsEmptyAdapterSet(t *testing.T) {
	if _, err := NewDiscoverer(); err == nil {
		t.Fatal("NewDiscoverer() error = nil, want non-nil")
	}
}

func TestDiscovererRejectsInvalidAdapterDeclarations(t *testing.T) {
	valid := discovererTestCoverage()
	truncated := discovererTestCoverage()
	truncated.Truncated = true
	truncated.Notes = append(truncated.Notes, Reason{Code: ReasonResultsTruncated, Message: "synthetic truncation"})
	truncated.Notes = SortReasons(truncated.Notes)

	var nilAdapter Adapter
	var typedNil *discovererFakeAdapter
	tests := []struct {
		name     string
		adapters []Adapter
	}{
		{name: "nil interface", adapters: []Adapter{nilAdapter}},
		{name: "typed nil pointer", adapters: []Adapter{typedNil}},
		{name: "unknown provider", adapters: []Adapter{&discovererFakeAdapter{provider: "synthetic-provider", coverage: valid}}},
		{name: "duplicate provider", adapters: []Adapter{
			&discovererFakeAdapter{provider: ProviderRunPod, coverage: valid},
			&discovererFakeAdapter{provider: ProviderRunPod, coverage: valid},
		}},
		{name: "invalid coverage", adapters: []Adapter{&discovererFakeAdapter{provider: ProviderRunPod}}},
		{name: "complete baseline", adapters: []Adapter{&discovererFakeAdapter{
			provider: ProviderRunPod,
			coverage: Coverage{Market: "on-demand", Complete: true},
		}}},
		{name: "truncated baseline", adapters: []Adapter{&discovererFakeAdapter{provider: ProviderRunPod, coverage: truncated}}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewDiscoverer(test.adapters...); err == nil {
				t.Fatal("NewDiscoverer() error = nil, want non-nil")
			}
		})
	}
}

func TestDiscovererCapturesImmutableAdapterDeclarationsOnce(t *testing.T) {
	coverage := discovererTestCoverage()
	adapter := &discovererFakeAdapter{provider: ProviderVastAI, coverage: coverage}

	discoverer, err := NewDiscoverer(adapter)
	if err != nil {
		t.Fatalf("NewDiscoverer() error = %v, want nil", err)
	}
	if adapter.providerCalls != 1 || adapter.coverageCalls != 1 {
		t.Fatalf("declaration calls = Provider:%d Coverage:%d, want 1 each", adapter.providerCalls, adapter.coverageCalls)
	}

	coverage.Filters[0] = "mutated=true"
	coverage.Notes[0].Message = "mutated note"
	adapter.coverage = Coverage{Market: "mutated"}

	if got := discoverer.entries[0].coverage.Filters[0]; got != "gpu=true" {
		t.Errorf("stored coverage filter = %q, want %q", got, "gpu=true")
	}
	if got := discoverer.entries[0].coverage.Notes[0].Message; got != "synthetic coverage note" {
		t.Errorf("stored coverage note = %q, want %q", got, "synthetic coverage note")
	}
	_ = discoverer.Discover(context.Background())
	if adapter.providerCalls != 1 || adapter.coverageCalls != 1 {
		t.Fatalf("declaration calls after discovery = Provider:%d Coverage:%d, want 1 each", adapter.providerCalls, adapter.coverageCalls)
	}
}

func TestDiscovererStartsAllAdaptersConcurrentlyAndReturnsProviderOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := make(chan Provider, 4)
		releases := map[Provider]chan struct{}{
			ProviderRunPod:       make(chan struct{}),
			ProviderVastAI:       make(chan struct{}),
			ProviderLambda:       make(chan struct{}),
			ProviderDigitalOcean: make(chan struct{}),
		}
		adapterFor := func(provider Provider) Adapter {
			return &discovererFakeAdapter{
				provider: provider,
				coverage: discovererTestCoverage(),
				discover: func(context.Context) (Snapshot, error) {
					started <- provider
					<-releases[provider]
					return Snapshot{Coverage: discovererActualCoverage()}, nil
				},
			}
		}
		discoverer, err := NewDiscoverer(
			adapterFor(ProviderDigitalOcean),
			adapterFor(ProviderLambda),
			adapterFor(ProviderVastAI),
			adapterFor(ProviderRunPod),
		)
		if err != nil {
			t.Fatalf("NewDiscoverer() error = %v, want nil", err)
		}

		resultCh := make(chan []ProviderResult, 1)
		go func() { resultCh <- discoverer.Discover(context.Background()) }()
		synctest.Wait()
		if got := len(started); got != 4 {
			t.Fatalf("started adapters = %d, want 4 before any release", got)
		}

		for _, provider := range []Provider{ProviderDigitalOcean, ProviderLambda, ProviderVastAI, ProviderRunPod} {
			close(releases[provider])
			synctest.Wait()
		}
		results := <-resultCh
		wantOrder := []Provider{ProviderRunPod, ProviderVastAI, ProviderLambda, ProviderDigitalOcean}
		if len(results) != len(wantOrder) {
			t.Fatalf("Discover() result count = %d, want %d", len(results), len(wantOrder))
		}
		for index, wantProvider := range wantOrder {
			if results[index].Provider != wantProvider {
				t.Errorf("Discover() result[%d].Provider = %q, want %q", index, results[index].Provider, wantProvider)
			}
			if results[index].Status != ResultSuccess {
				t.Errorf("Discover() result[%d].Status = %q, want %q", index, results[index].Status, ResultSuccess)
			}
			if results[index].Dispositions == nil {
				t.Errorf("Discover() result[%d].Dispositions = nil, want empty slice", index)
			}
		}
	})
}

func TestDiscovererClassifiesProviderOutcomesIndependently(t *testing.T) {
	truncated := discovererActualCoverage()
	truncated.Complete = false
	truncated.Truncated = true
	truncated.Notes = SortReasons(append(truncated.Notes, Reason{
		Code:    ReasonResultsTruncated,
		Message: "synthetic truncation",
	}))
	disposition, err := Rejected(ProviderLambda, "offer-2", Reason{
		Code:    ReasonUnavailable,
		Message: "synthetic unavailable",
	})
	if err != nil {
		t.Fatalf("Rejected() error = %v", err)
	}

	adapters := []Adapter{
		&discovererFakeAdapter{provider: ProviderRunPod, coverage: discovererTestCoverage(), discover: func(context.Context) (Snapshot, error) {
			return Snapshot{Coverage: discovererActualCoverage()}, nil
		}},
		&discovererFakeAdapter{provider: ProviderVastAI, coverage: discovererTestCoverage(), discover: func(context.Context) (Snapshot, error) {
			return Snapshot{Coverage: truncated}, nil
		}},
		&discovererFakeAdapter{provider: ProviderLambda, coverage: discovererTestCoverage(), discover: func(context.Context) (Snapshot, error) {
			return Snapshot{Dispositions: []Disposition{disposition}, Coverage: discovererActualCoverage()}, AdapterError{
				Provider: ProviderLambda,
				Code:     FailureRateLimited,
			}
		}},
		&discovererFakeAdapter{provider: ProviderDigitalOcean, coverage: discovererTestCoverage(), discover: func(context.Context) (Snapshot, error) {
			return Snapshot{}, &AdapterError{Provider: ProviderDigitalOcean, Code: FailureNotConfigured}
		}},
	}
	discoverer, err := NewDiscoverer(adapters...)
	if err != nil {
		t.Fatalf("NewDiscoverer() error = %v, want nil", err)
	}

	results := discoverer.Discover(context.Background())
	wantStatuses := []ResultStatus{ResultSuccess, ResultPartial, ResultPartial, ResultSkipped}
	for index, wantStatus := range wantStatuses {
		if results[index].Status != wantStatus {
			t.Errorf("Discover() result[%d].Status = %q, want %q", index, results[index].Status, wantStatus)
		}
	}
	if results[2].Failure == nil || results[2].Failure.Code != FailureRateLimited {
		t.Errorf("partial failure = %#v, want rate_limited", results[2].Failure)
	}
	if results[3].Failure == nil || results[3].Failure.Code != FailureNotConfigured {
		t.Errorf("skipped failure = %#v, want not_configured", results[3].Failure)
	}
}

func TestDiscovererDoesNotCancelPeersAfterAdapterFailure(t *testing.T) {
	completed := make(chan struct{}, 1)
	discoverer, err := NewDiscoverer(
		&discovererFakeAdapter{provider: ProviderRunPod, coverage: discovererTestCoverage(), discover: func(context.Context) (Snapshot, error) {
			return Snapshot{}, errors.New("synthetic adapter failure")
		}},
		&discovererFakeAdapter{provider: ProviderVastAI, coverage: discovererTestCoverage(), discover: func(context.Context) (Snapshot, error) {
			completed <- struct{}{}
			return Snapshot{Coverage: discovererActualCoverage()}, nil
		}},
	)
	if err != nil {
		t.Fatalf("NewDiscoverer() error = %v, want nil", err)
	}

	results := discoverer.Discover(context.Background())
	if len(completed) != 1 {
		t.Fatal("peer adapter did not complete after another adapter failed")
	}
	if results[0].Status != ResultFailed || results[1].Status != ResultSuccess {
		t.Fatalf("Discover() statuses = [%q %q], want [failed success]", results[0].Status, results[1].Status)
	}
}

// discovererControlledContext is a coordinator-check test double: Err changes
// at a configured check while Done stays nil. Active Done is covered separately.
type discovererControlledContext struct {
	cancelAt int32
	err      error
	checks   atomic.Int32
}

func (*discovererControlledContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (*discovererControlledContext) Done() <-chan struct{}       { return nil }
func (*discovererControlledContext) Value(any) any               { return nil }

func (c *discovererControlledContext) Err() error {
	if c.checks.Add(1) >= c.cancelAt {
		return c.err
	}
	return nil
}

func TestDiscovererRejectsNilAndAlreadyCanceledContextsWithoutStartingAdapters(t *testing.T) {
	var calls atomic.Int32
	newAdapter := func(provider Provider) Adapter {
		return &discovererFakeAdapter{provider: provider, coverage: discovererTestCoverage(), discover: func(context.Context) (Snapshot, error) {
			calls.Add(1)
			return Snapshot{Coverage: discovererActualCoverage()}, nil
		}}
	}
	discoverer, err := NewDiscoverer(newAdapter(ProviderRunPod), newAdapter(ProviderVastAI))
	if err != nil {
		t.Fatalf("NewDiscoverer() error = %v, want nil", err)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	deadline, cancelDeadline := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer cancelDeadline()
	tests := []struct {
		name     string
		ctx      context.Context
		wantCode FailureCode
	}{
		{name: "nil", ctx: nil, wantCode: FailureRequest},
		{name: "canceled", ctx: canceled, wantCode: FailureRequest},
		{name: "deadline", ctx: deadline, wantCode: FailureTimeout},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			results := discoverer.Discover(test.ctx)
			if got := calls.Load(); got != 0 {
				t.Fatalf("adapter calls = %d, want 0", got)
			}
			for _, result := range results {
				if result.Status != ResultFailed || result.Failure == nil || result.Failure.Code != test.wantCode {
					t.Errorf("Discover() result = %#v, want failed %q", result, test.wantCode)
				}
				if result.Dispositions == nil {
					t.Error("Discover() Dispositions = nil, want empty slice")
				}
				if err := result.Failure.validate(); err != nil {
					t.Errorf("failure validation error = %v", err)
				}
				if _, err := json.Marshal(result); err != nil {
					t.Errorf("json.Marshal(ProviderResult) error = %v", err)
				}
			}
		})
	}
}

func TestDiscovererCancellationAfterReceiveDiscardsOutcome(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		adapter := &discovererFakeAdapter{provider: ProviderRunPod, coverage: discovererTestCoverage(), discover: func(context.Context) (Snapshot, error) {
			<-release
			return Snapshot{Coverage: discovererActualCoverage()}, nil
		}}
		discoverer, err := NewDiscoverer(adapter)
		if err != nil {
			t.Fatalf("NewDiscoverer() error = %v, want nil", err)
		}
		ctx := &discovererControlledContext{cancelAt: 3, err: context.Canceled}
		resultCh := make(chan []ProviderResult, 1)
		go func() { resultCh <- discoverer.Discover(ctx) }()
		synctest.Wait()
		close(release)
		synctest.Wait()

		result := (<-resultCh)[0]
		if result.Status != ResultFailed || result.Failure == nil || result.Failure.Code != FailureRequest {
			t.Fatalf("Discover() result = %#v, want canceled request failure", result)
		}
	})
}

func TestDiscovererChecksCancellationBeforeWaitingForOutcomes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := make(chan struct{}, 2)
		newAdapter := func(provider Provider) Adapter {
			return &discovererFakeAdapter{provider: provider, coverage: discovererTestCoverage(), discover: func(context.Context) (Snapshot, error) {
				started <- struct{}{}
				return Snapshot{Coverage: discovererActualCoverage()}, nil
			}}
		}
		discoverer, err := NewDiscoverer(newAdapter(ProviderRunPod), newAdapter(ProviderVastAI))
		if err != nil {
			t.Fatalf("NewDiscoverer() error = %v, want nil", err)
		}
		ctx := &discovererControlledContext{cancelAt: 2, err: context.Canceled}
		results := discoverer.Discover(ctx)
		synctest.Wait()
		if len(started) != 2 {
			t.Fatalf("started adapters = %d, want 2", len(started))
		}
		for _, result := range results {
			if result.Status != ResultFailed || result.Failure == nil || result.Failure.Code != FailureRequest {
				t.Errorf("Discover() result = %#v, want request failure", result)
			}
		}
	})
}

func TestDiscovererReturnsOnActiveContextCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := make(chan struct{})
		release := make(chan struct{})
		t.Cleanup(func() { close(release) })
		discoverer, err := NewDiscoverer(&discovererFakeAdapter{
			provider: ProviderRunPod,
			coverage: discovererTestCoverage(),
			discover: func(context.Context) (Snapshot, error) {
				started <- struct{}{}
				<-release
				return Snapshot{Coverage: discovererActualCoverage()}, nil
			},
		})
		if err != nil {
			t.Fatalf("NewDiscoverer() error = %v, want nil", err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		resultCh := make(chan []ProviderResult, 1)
		go func() { resultCh <- discoverer.Discover(ctx) }()
		<-started
		synctest.Wait()
		cancel()
		synctest.Wait()

		select {
		case results := <-resultCh:
			result := results[0]
			if result.Status != ResultFailed || result.Failure == nil || result.Failure.Code != FailureRequest {
				t.Fatalf("Discover() result = %#v, want request failure", result)
			}
		default:
			t.Fatal("Discover() did not return after active context cancellation")
		}
	})
}

func TestDiscovererRetainsLiveOutcomeAndDoesNotWaitForLateAdapter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		runPodRelease := make(chan struct{})
		vastRelease := make(chan struct{})
		defer close(vastRelease)
		discoverer, err := NewDiscoverer(
			&discovererFakeAdapter{provider: ProviderRunPod, coverage: discovererTestCoverage(), discover: func(context.Context) (Snapshot, error) {
				<-runPodRelease
				return Snapshot{Coverage: discovererActualCoverage()}, nil
			}},
			&discovererFakeAdapter{provider: ProviderVastAI, coverage: discovererTestCoverage(), discover: func(context.Context) (Snapshot, error) {
				<-vastRelease
				return Snapshot{Coverage: discovererActualCoverage()}, nil
			}},
		)
		if err != nil {
			t.Fatalf("NewDiscoverer() error = %v, want nil", err)
		}
		ctx := &discovererControlledContext{cancelAt: 4, err: context.DeadlineExceeded}
		resultCh := make(chan []ProviderResult, 1)
		go func() { resultCh <- discoverer.Discover(ctx) }()
		synctest.Wait()
		close(runPodRelease)
		synctest.Wait()

		select {
		case results := <-resultCh:
			if results[0].Status != ResultSuccess {
				t.Errorf("retained result status = %q, want success", results[0].Status)
			}
			if results[1].Status != ResultFailed || results[1].Failure == nil || results[1].Failure.Code != FailureTimeout {
				t.Errorf("unfinished result = %#v, want timeout failure", results[1])
			}
		default:
			t.Fatal("Discover() waited for a context-ignoring adapter")
		}
	})
}

func TestDiscovererFailsClosedOnInvalidSnapshots(t *testing.T) {
	validCoverage := discovererActualCoverage()
	validDisposition, err := Rejected(ProviderRunPod, "synthetic-source", Reason{
		Code:    ReasonUnavailable,
		Message: "synthetic unavailable",
	})
	if err != nil {
		t.Fatalf("Rejected() error = %v", err)
	}
	tests := []struct {
		name       string
		provider   Provider
		snapshot   Snapshot
		adapterErr error
	}{
		{name: "zero clean snapshot", provider: ProviderRunPod, snapshot: Snapshot{}},
		{name: "coverage", provider: ProviderRunPod, snapshot: Snapshot{Coverage: Coverage{Market: "raw-secret", Complete: true, Truncated: true}}},
		{name: "provider mismatch", provider: ProviderVastAI, snapshot: Snapshot{Dispositions: []Disposition{validDisposition}, Coverage: validCoverage}},
		{name: "disposition", provider: ProviderRunPod, snapshot: Snapshot{Dispositions: []Disposition{{
			Provider: ProviderRunPod,
			SourceID: "raw-secret",
			Status:   "synthetic-status",
		}}, Coverage: validCoverage}},
		{name: "warning", provider: ProviderRunPod, snapshot: Snapshot{Warnings: []Reason{{
			Code:    "synthetic-reason",
			Message: "raw-secret",
		}}, Coverage: validCoverage}},
		{name: "partial coverage", provider: ProviderRunPod, snapshot: Snapshot{Dispositions: []Disposition{validDisposition}}, adapterErr: errors.New("raw-secret partial error")},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			discoverer, err := NewDiscoverer(&discovererFakeAdapter{
				provider: test.provider,
				coverage: discovererTestCoverage(),
				discover: func(context.Context) (Snapshot, error) { return test.snapshot, test.adapterErr },
			})
			if err != nil {
				t.Fatalf("NewDiscoverer() error = %v, want nil", err)
			}

			result := discoverer.Discover(context.Background())[0]
			if result.Status != ResultFailed || result.Failure == nil || result.Failure.Code != FailureInvalidResponse {
				t.Fatalf("Discover() result = %#v, want invalid_response failure", result)
			}
			if len(result.Dispositions) != 0 || len(result.Warnings) != 0 {
				t.Errorf("invalid snapshot content retained: %#v", result)
			}
			if result.Coverage.Market != discovererTestCoverage().Market {
				t.Errorf("failure coverage market = %q, want baseline %q", result.Coverage.Market, discovererTestCoverage().Market)
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatalf("json.Marshal(ProviderResult) error = %v", err)
			}
			if strings.Contains(string(encoded), "raw-secret") || strings.Contains(string(encoded), "synthetic-status") || strings.Contains(string(encoded), "synthetic-reason") {
				t.Fatalf("invalid snapshot leaked into result JSON: %s", encoded)
			}
		})
	}
}

func TestDiscovererRejectsDuplicateDispositionIdentity(t *testing.T) {
	firstOffer := validOffer()
	secondOffer := validOffer()
	secondOffer.GPUModel = "Conflicting Example GPU"
	firstAccepted, err := Accepted(firstOffer)
	if err != nil {
		t.Fatalf("Accepted(first offer) error = %v", err)
	}
	secondAccepted, err := Accepted(secondOffer)
	if err != nil {
		t.Fatalf("Accepted(second offer) error = %v", err)
	}
	rejected, err := Rejected(ProviderDigitalOcean, firstOffer.ProviderOfferID, Reason{
		Code:    ReasonUnavailable,
		Message: "synthetic unavailable",
	})
	if err != nil {
		t.Fatalf("Rejected() error = %v", err)
	}

	tests := []struct {
		name         string
		dispositions []Disposition
	}{
		{name: "conflicting accepted forward", dispositions: []Disposition{firstAccepted, secondAccepted}},
		{name: "conflicting accepted reverse", dispositions: []Disposition{secondAccepted, firstAccepted}},
		{name: "accepted and rejected forward", dispositions: []Disposition{firstAccepted, rejected}},
		{name: "accepted and rejected reverse", dispositions: []Disposition{rejected, firstAccepted}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, disposition := range test.dispositions {
				if err := disposition.Validate(); err != nil {
					t.Fatalf("input disposition validation error = %v", err)
				}
			}
			discoverer, err := NewDiscoverer(&discovererFakeAdapter{
				provider: ProviderDigitalOcean,
				coverage: discovererTestCoverage(),
				discover: func(context.Context) (Snapshot, error) {
					return Snapshot{Dispositions: test.dispositions, Coverage: discovererActualCoverage()}, nil
				},
			})
			if err != nil {
				t.Fatalf("NewDiscoverer() error = %v, want nil", err)
			}

			result := discoverer.Discover(context.Background())[0]
			if result.Status != ResultFailed || result.Failure == nil || result.Failure.Code != FailureInvalidResponse {
				t.Fatalf("Discover() result = %#v, want invalid_response failure", result)
			}
			if len(result.Dispositions) != 0 || result.Coverage.Market != discovererTestCoverage().Market {
				t.Fatalf("Discover() retained duplicate snapshot content: %#v", result)
			}
		})
	}
}

func TestDiscovererSortsNestedOfferWarningsWithoutMutatingSnapshot(t *testing.T) {
	offer := validOffer()
	offer.Warnings = []Reason{
		{Code: ReasonRegionUnknown, Message: "synthetic unknown region"},
		{Code: ReasonCoverageLimited, Message: "synthetic limited coverage"},
	}
	disposition := Disposition{
		Provider: offer.Provider,
		SourceID: offer.ProviderOfferID,
		Status:   DispositionAccepted,
		Offer:    &offer,
	}
	if err := disposition.Validate(); err != nil {
		t.Fatalf("input disposition validation error = %v", err)
	}
	snapshot := Snapshot{
		Dispositions: []Disposition{disposition},
		Coverage:     discovererActualCoverage(),
	}
	discoverer, err := NewDiscoverer(&discovererFakeAdapter{
		provider: ProviderDigitalOcean,
		coverage: discovererTestCoverage(),
		discover: func(context.Context) (Snapshot, error) {
			return snapshot, nil
		},
	})
	if err != nil {
		t.Fatalf("NewDiscoverer() error = %v, want nil", err)
	}

	result := discoverer.Discover(context.Background())[0]
	if result.Status != ResultSuccess {
		t.Fatalf("Discover() status = %q, want success", result.Status)
	}
	gotWarnings := result.Dispositions[0].Offer.Warnings
	if gotWarnings[0].Code != ReasonCoverageLimited || gotWarnings[1].Code != ReasonRegionUnknown {
		t.Fatalf("nested offer warnings = %#v, want sorted", gotWarnings)
	}
	if snapshot.Dispositions[0].Offer.Warnings[0].Code != ReasonRegionUnknown {
		t.Fatalf("Discover() mutated adapter snapshot warnings: %#v", snapshot.Dispositions[0].Offer.Warnings)
	}
	gotWarnings[0].Message = "returned mutation"
	if snapshot.Dispositions[0].Offer.Warnings[1].Message != "synthetic limited coverage" {
		t.Fatalf("returned warning mutation reached adapter snapshot: %#v", snapshot.Dispositions[0].Offer.Warnings)
	}
}

func TestDiscovererNormalizesAdapterErrorsWithoutLeakingRawText(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want AdapterError
	}{
		{
			name: "matching pointer",
			err:  &AdapterError{Provider: ProviderRunPod, Code: FailureRateLimited, HTTPStatus: 429, RetryAfterSeconds: 17},
			want: AdapterError{Provider: ProviderRunPod, Code: FailureRateLimited, HTTPStatus: 429, RetryAfterSeconds: 17},
		},
		{
			name: "matching value",
			err:  AdapterError{Provider: ProviderRunPod, Code: FailureAuthentication, HTTPStatus: 401},
			want: AdapterError{Provider: ProviderRunPod, Code: FailureAuthentication, HTTPStatus: 401},
		},
		{
			name: "wrapped matching pointer",
			err:  fmt.Errorf("raw-secret: %w", &AdapterError{Provider: ProviderRunPod, Code: FailureProviderUnavailable, HTTPStatus: 503}),
			want: AdapterError{Provider: ProviderRunPod, Code: FailureProviderUnavailable, HTTPStatus: 503},
		},
		{name: "transport sentinel", err: AdapterError{Code: FailureRequest}, want: AdapterError{Provider: ProviderRunPod, Code: FailureRequest}},
		{name: "invalid adapter error", err: AdapterError{Provider: ProviderRunPod, Code: "raw-secret-code", HTTPStatus: 429, RetryAfterSeconds: 9}, want: AdapterError{Provider: ProviderRunPod, Code: FailureRequest}},
		{name: "provider mismatch", err: AdapterError{Provider: ProviderVastAI, Code: FailureRateLimited, HTTPStatus: 429, RetryAfterSeconds: 9}, want: AdapterError{Provider: ProviderRunPod, Code: FailureRequest}},
		{name: "arbitrary", err: errors.New("raw-secret arbitrary error"), want: AdapterError{Provider: ProviderRunPod, Code: FailureRequest}},
		{name: "wrapped arbitrary", err: fmt.Errorf("raw-secret wrapper: %w", errors.New("raw-secret cause")), want: AdapterError{Provider: ProviderRunPod, Code: FailureRequest}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			discoverer, err := NewDiscoverer(&discovererFakeAdapter{
				provider: ProviderRunPod,
				coverage: discovererTestCoverage(),
				discover: func(context.Context) (Snapshot, error) { return Snapshot{}, test.err },
			})
			if err != nil {
				t.Fatalf("NewDiscoverer() error = %v, want nil", err)
			}
			result := discoverer.Discover(context.Background())[0]
			if result.Status != ResultFailed || result.Failure == nil || *result.Failure != test.want {
				t.Fatalf("Discover() failure = %#v, want %#v", result.Failure, test.want)
			}
			if err := result.Failure.validate(); err != nil {
				t.Fatalf("failure validation error = %v", err)
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatalf("json.Marshal(ProviderResult) error = %v", err)
			}
			if strings.Contains(string(encoded), "raw-secret") {
				t.Fatalf("normalized failure leaked raw text: %s", encoded)
			}
		})
	}
}

func TestDiscovererRejectsNotConfiguredFailureWithDispositions(t *testing.T) {
	disposition, err := Rejected(ProviderRunPod, "synthetic-source", Reason{
		Code:    ReasonUnavailable,
		Message: "synthetic unavailable",
	})
	if err != nil {
		t.Fatalf("Rejected() error = %v", err)
	}
	discoverer, err := NewDiscoverer(&discovererFakeAdapter{
		provider: ProviderRunPod,
		coverage: discovererTestCoverage(),
		discover: func(context.Context) (Snapshot, error) {
			return Snapshot{Dispositions: []Disposition{disposition}, Coverage: discovererActualCoverage()},
				&AdapterError{Provider: ProviderRunPod, Code: FailureNotConfigured}
		},
	})
	if err != nil {
		t.Fatalf("NewDiscoverer() error = %v, want nil", err)
	}

	result := discoverer.Discover(context.Background())[0]
	if result.Status != ResultFailed || result.Failure == nil || result.Failure.Code != FailureInvalidResponse {
		t.Fatalf("Discover() result = %#v, want invalid_response failure", result)
	}
	if len(result.Dispositions) != 0 {
		t.Errorf("Discover() retained dispositions = %#v, want none", result.Dispositions)
	}
}

func TestDiscovererPreservesValidDispositionsWithArbitraryError(t *testing.T) {
	disposition, err := Rejected(ProviderRunPod, "synthetic-source", Reason{
		Code:    ReasonUnavailable,
		Message: "synthetic unavailable",
	})
	if err != nil {
		t.Fatalf("Rejected() error = %v", err)
	}
	discoverer, err := NewDiscoverer(&discovererFakeAdapter{
		provider: ProviderRunPod,
		coverage: discovererTestCoverage(),
		discover: func(context.Context) (Snapshot, error) {
			return Snapshot{Dispositions: []Disposition{disposition}, Coverage: discovererActualCoverage()},
				errors.New("raw-secret arbitrary error")
		},
	})
	if err != nil {
		t.Fatalf("NewDiscoverer() error = %v, want nil", err)
	}

	result := discoverer.Discover(context.Background())[0]
	if result.Status != ResultPartial || len(result.Dispositions) != 1 {
		t.Fatalf("Discover() result = %#v, want partial with one disposition", result)
	}
	if result.Failure == nil || *result.Failure != (AdapterError{Provider: ProviderRunPod, Code: FailureRequest}) {
		t.Fatalf("Discover() failure = %#v, want normalized request failure", result.Failure)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("json.Marshal(ProviderResult) error = %v", err)
	}
	if strings.Contains(string(encoded), "raw-secret") {
		t.Fatalf("partial result leaked raw error: %s", encoded)
	}
}

func TestProviderResultJSONUsesArrayForEmptyDispositions(t *testing.T) {
	result := ProviderResult{
		Provider: ProviderRunPod,
		Status:   ResultSuccess,
		Coverage: discovererActualCoverage(),
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("json.Marshal(ProviderResult) error = %v", err)
	}
	if !strings.Contains(string(encoded), `"dispositions":[]`) {
		t.Fatalf("json.Marshal(ProviderResult) = %s, want empty dispositions array", encoded)
	}
	if strings.Contains(string(encoded), `"dispositions":null`) {
		t.Fatalf("json.Marshal(ProviderResult) = %s, must not contain null dispositions", encoded)
	}
}

func TestDiscovererDeepClonesSnapshotsAndStoredBaseline(t *testing.T) {
	offer := validOffer()
	accepted, err := Accepted(offer)
	if err != nil {
		t.Fatalf("Accepted() error = %v", err)
	}
	rejected, err := Rejected(ProviderDigitalOcean, "aaa-source", Reason{
		Code:    ReasonUnavailable,
		Message: "synthetic unavailable",
	})
	if err != nil {
		t.Fatalf("Rejected() error = %v", err)
	}
	snapshot := Snapshot{
		Dispositions: []Disposition{accepted, rejected},
		Warnings: []Reason{
			{Code: ReasonRegionUnknown, Message: "synthetic unknown region"},
			{Code: ReasonCoverageLimited, Message: "synthetic limited coverage"},
		},
		Coverage: discovererActualCoverage(),
	}
	var adapterErr error
	adapter := &discovererFakeAdapter{
		provider: ProviderDigitalOcean,
		coverage: discovererTestCoverage(),
		discover: func(context.Context) (Snapshot, error) { return snapshot, adapterErr },
	}
	discoverer, err := NewDiscoverer(adapter)
	if err != nil {
		t.Fatalf("NewDiscoverer() error = %v, want nil", err)
	}

	first := discoverer.Discover(context.Background())[0]
	if first.Dispositions[0].SourceID != "aaa-source" || first.Dispositions[1].SourceID != offer.ProviderOfferID {
		t.Fatalf("sorted disposition IDs = [%q %q], want [aaa-source %s]", first.Dispositions[0].SourceID, first.Dispositions[1].SourceID, offer.ProviderOfferID)
	}
	if first.Warnings[0].Code != ReasonCoverageLimited || first.Warnings[1].Code != ReasonRegionUnknown {
		t.Fatalf("sorted warnings = %#v", first.Warnings)
	}

	first.Dispositions[0].Reasons[0].Message = "returned mutation"
	first.Dispositions[1].Offer.GPUModel = "returned mutation"
	first.Dispositions[1].Offer.Billing.Components[0].Name = "returned mutation"
	*first.Dispositions[1].Offer.ProviderSourceTime = time.Unix(2, 0).UTC()
	first.Warnings[0].Message = "returned mutation"
	first.Coverage.Filters[0] = "returned mutation"
	first.Coverage.Notes[0].Message = "returned mutation"
	if snapshot.Dispositions[0].Offer.GPUModel != offer.GPUModel || snapshot.Dispositions[1].Reasons[0].Message != "synthetic unavailable" {
		t.Fatalf("returned result mutated adapter snapshot: %#v", snapshot)
	}

	second := discoverer.Discover(context.Background())[0]
	snapshot.Dispositions[0].Offer.GPUModel = "adapter mutation"
	snapshot.Dispositions[0].Offer.Billing.Components[0].Name = "adapter mutation"
	*snapshot.Dispositions[0].Offer.ProviderSourceTime = time.Unix(3, 0).UTC()
	snapshot.Dispositions[1].Reasons[0].Message = "adapter mutation"
	snapshot.Warnings[0].Message = "adapter mutation"
	snapshot.Coverage.Filters[0] = "adapter mutation"
	snapshot.Coverage.Notes[0].Message = "adapter mutation"
	if second.Dispositions[1].Offer.GPUModel != offer.GPUModel || second.Dispositions[1].Offer.Billing.Components[0].Name != "compute" {
		t.Errorf("adapter snapshot mutation reached returned offer: %#v", second.Dispositions[1].Offer)
	}
	if second.Dispositions[0].Reasons[0].Message != "synthetic unavailable" || second.Warnings[1].Message != "synthetic unknown region" {
		t.Errorf("adapter snapshot mutation reached returned reasons: %#v", second)
	}
	if second.Coverage.Filters[0] != "gpu=true" || second.Coverage.Notes[0].Message != "synthetic coverage note" {
		t.Errorf("adapter snapshot mutation reached returned coverage: %#v", second.Coverage)
	}

	adapterErr = errors.New("synthetic failure")
	snapshot = Snapshot{}
	baselineFailure := discoverer.Discover(context.Background())[0]
	baselineFailure.Coverage.Filters[0] = "returned baseline mutation"
	baselineFailure.Coverage.Notes[0].Message = "returned baseline mutation"
	nextFailure := discoverer.Discover(context.Background())[0]
	if nextFailure.Coverage.Filters[0] != "gpu=true" || nextFailure.Coverage.Notes[0].Message != "synthetic coverage note" {
		t.Errorf("returned result mutated stored baseline: %#v", nextFailure.Coverage)
	}
}

func TestDiscovererDefensivelyCopiesAdapterFailures(t *testing.T) {
	adapterFailure := &AdapterError{
		Provider:          ProviderRunPod,
		Code:              FailureRateLimited,
		HTTPStatus:        429,
		RetryAfterSeconds: 13,
	}
	discoverer, err := NewDiscoverer(&discovererFakeAdapter{
		provider: ProviderRunPod,
		coverage: discovererTestCoverage(),
		discover: func(context.Context) (Snapshot, error) { return Snapshot{}, adapterFailure },
	})
	if err != nil {
		t.Fatalf("NewDiscoverer() error = %v, want nil", err)
	}

	result := discoverer.Discover(context.Background())[0]
	if result.Failure == adapterFailure {
		t.Fatal("Discover() reused adapter failure pointer")
	}
	adapterFailure.Code = FailureAuthentication
	adapterFailure.HTTPStatus = 401
	adapterFailure.RetryAfterSeconds = 0
	if result.Failure.Code != FailureRateLimited || result.Failure.HTTPStatus != 429 || result.Failure.RetryAfterSeconds != 13 {
		t.Errorf("adapter failure mutation reached result: %#v", result.Failure)
	}
	result.Failure.Code = FailureTimeout
	if adapterFailure.Code != FailureAuthentication {
		t.Errorf("result failure mutation reached adapter error: %#v", adapterFailure)
	}
}
