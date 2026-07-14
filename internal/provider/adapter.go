package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
)

type Snapshot struct {
	Dispositions []Disposition `json:"dispositions"`
	Warnings     []Reason      `json:"warnings,omitempty"`
	Coverage     Coverage      `json:"coverage"`
}

type Adapter interface {
	Provider() Provider
	Coverage() Coverage
	Discover(context.Context) (Snapshot, error)
}

type ResultStatus string

const (
	ResultSuccess ResultStatus = "success"
	ResultPartial ResultStatus = "partial"
	ResultFailed  ResultStatus = "failed"
	ResultSkipped ResultStatus = "skipped"
)

type ProviderResult struct {
	Provider     Provider      `json:"provider"`
	Status       ResultStatus  `json:"status"`
	Dispositions []Disposition `json:"dispositions"`
	Warnings     []Reason      `json:"warnings,omitempty"`
	Coverage     Coverage      `json:"coverage"`
	Failure      *AdapterError `json:"failure,omitempty"`
}

func (r ProviderResult) MarshalJSON() ([]byte, error) {
	if r.Dispositions == nil {
		r.Dispositions = make([]Disposition, 0)
	}
	type providerResultJSON ProviderResult
	return json.Marshal(providerResultJSON(r))
}

type adapterEntry struct {
	adapter  Adapter
	provider Provider
	coverage Coverage
}

type Discoverer struct {
	entries []adapterEntry
}

func NewDiscoverer(adapters ...Adapter) (*Discoverer, error) {
	if len(adapters) == 0 {
		return nil, errors.New("at least one adapter is required")
	}

	entries := make([]adapterEntry, 0, len(adapters))
	seen := make(map[Provider]struct{}, len(adapters))
	for _, adapter := range adapters {
		if isNilAdapter(adapter) {
			return nil, errors.New("adapter must not be nil")
		}
		provider := adapter.Provider()
		if err := validateProvider(provider); err != nil {
			return nil, errors.New("adapter provider must be recognized")
		}
		if _, duplicate := seen[provider]; duplicate {
			return nil, errors.New("adapter provider must be unique")
		}
		coverage := adapter.Coverage()
		if err := coverage.Validate(); err != nil || coverage.Complete || coverage.Truncated {
			return nil, errors.New("adapter baseline coverage must be valid and incomplete")
		}
		seen[provider] = struct{}{}
		entries = append(entries, adapterEntry{
			adapter:  adapter,
			provider: provider,
			coverage: cloneCoverage(coverage),
		})
	}

	sort.Slice(entries, func(left, right int) bool {
		return providerOrder(entries[left].provider) < providerOrder(entries[right].provider)
	})
	return &Discoverer{entries: entries}, nil
}

func isNilAdapter(adapter Adapter) bool {
	if adapter == nil {
		return true
	}
	value := reflect.ValueOf(adapter)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func providerOrder(provider Provider) int {
	switch provider {
	case ProviderRunPod:
		return 0
	case ProviderVastAI:
		return 1
	case ProviderLambda:
		return 2
	case ProviderDigitalOcean:
		return 3
	default:
		return 4
	}
}

func cloneCoverage(coverage Coverage) Coverage {
	cloned := coverage
	cloned.Filters = append([]string(nil), coverage.Filters...)
	cloned.Notes = append([]Reason(nil), coverage.Notes...)
	return cloned
}

type adapterOutcome struct {
	index    int
	snapshot Snapshot
	err      error
}

type dispositionIdentity struct {
	provider Provider
	sourceID string
}

func (d *Discoverer) Discover(ctx context.Context) []ProviderResult {
	if ctx == nil {
		return d.contextFailureResults(nil)
	}
	if err := ctx.Err(); err != nil {
		return d.contextFailureResults(err)
	}

	outcomes := make(chan adapterOutcome, len(d.entries))
	for index, entry := range d.entries {
		go func() {
			snapshot, err := entry.adapter.Discover(ctx)
			outcomes <- adapterOutcome{index: index, snapshot: snapshot, err: err}
		}()
	}

	results := make([]ProviderResult, len(d.entries))
	finished := make([]bool, len(d.entries))
	remaining := len(d.entries)
	for remaining > 0 {
		if err := ctx.Err(); err != nil {
			return d.completeContextFailures(results, finished, err)
		}
		select {
		case outcome := <-outcomes:
			if err := ctx.Err(); err != nil {
				return d.completeContextFailures(results, finished, err)
			}
			entry := d.entries[outcome.index]
			results[outcome.index] = normalizeOutcome(entry, outcome.snapshot, outcome.err)
			finished[outcome.index] = true
			remaining--
		case <-ctx.Done():
			return d.completeContextFailures(results, finished, ctx.Err())
		}
	}
	return results
}

func (d *Discoverer) contextFailureResults(err error) []ProviderResult {
	return d.completeContextFailures(
		make([]ProviderResult, len(d.entries)),
		make([]bool, len(d.entries)),
		err,
	)
}

func (d *Discoverer) completeContextFailures(results []ProviderResult, finished []bool, err error) []ProviderResult {
	code := FailureRequest
	if errors.Is(err, context.DeadlineExceeded) {
		code = FailureTimeout
	}
	for index, entry := range d.entries {
		if !finished[index] {
			results[index] = resultForFailure(entry, &AdapterError{Provider: entry.provider, Code: code})
		}
	}
	return results
}

func normalizeOutcome(entry adapterEntry, snapshot Snapshot, err error) ProviderResult {
	if err != nil && isEmptySnapshot(snapshot) {
		failure := normalizeAdapterError(entry.provider, err)
		return resultForFailure(entry, failure)
	}
	if !isValidSnapshot(entry.provider, snapshot) {
		return resultForFailure(entry, &AdapterError{Provider: entry.provider, Code: FailureInvalidResponse})
	}

	dispositions := SortDispositions(snapshot.Dispositions)
	for index := range dispositions {
		if dispositions[index].Offer != nil {
			dispositions[index].Offer.Warnings = SortReasons(dispositions[index].Offer.Warnings)
		}
	}
	result := ProviderResult{
		Provider:     entry.provider,
		Dispositions: dispositions,
		Warnings:     SortReasons(snapshot.Warnings),
		Coverage:     cloneCoverage(snapshot.Coverage),
	}
	if err == nil {
		if result.Coverage.Truncated {
			result.Status = ResultPartial
		} else {
			result.Status = ResultSuccess
		}
		return result
	}

	failure := normalizeAdapterError(entry.provider, err)
	if failure.Code == FailureNotConfigured && len(result.Dispositions) != 0 {
		return resultForFailure(entry, &AdapterError{Provider: entry.provider, Code: FailureInvalidResponse})
	}
	result.Failure = failure
	if len(result.Dispositions) != 0 {
		result.Status = ResultPartial
	} else if failure.Code == FailureNotConfigured {
		result.Status = ResultSkipped
	} else {
		result.Status = ResultFailed
	}
	return result
}

func isEmptySnapshot(snapshot Snapshot) bool {
	return len(snapshot.Dispositions) == 0 && len(snapshot.Warnings) == 0 && isZeroCoverage(snapshot.Coverage)
}

func isZeroCoverage(coverage Coverage) bool {
	return coverage.Market == "" && len(coverage.Filters) == 0 && coverage.Limit == 0 &&
		!coverage.Complete && !coverage.Truncated && len(coverage.Notes) == 0
}

func isValidSnapshot(provider Provider, snapshot Snapshot) bool {
	if err := snapshot.Coverage.Validate(); err != nil {
		return false
	}
	seen := make(map[dispositionIdentity]struct{}, len(snapshot.Dispositions))
	for _, disposition := range snapshot.Dispositions {
		if disposition.Provider != provider || disposition.Validate() != nil {
			return false
		}
		identity := dispositionIdentity{provider: disposition.Provider, sourceID: disposition.SourceID}
		if _, duplicate := seen[identity]; duplicate {
			return false
		}
		seen[identity] = struct{}{}
	}
	for _, warning := range snapshot.Warnings {
		if warning.validate() != nil {
			return false
		}
	}
	return true
}

func normalizeAdapterError(provider Provider, err error) *AdapterError {
	var pointer *AdapterError
	if errors.As(err, &pointer) && pointer != nil {
		return validatedAdapterError(provider, *pointer)
	}
	var value AdapterError
	if errors.As(err, &value) {
		return validatedAdapterError(provider, value)
	}
	return &AdapterError{Provider: provider, Code: FailureRequest}
}

func validatedAdapterError(provider Provider, adapterErr AdapterError) *AdapterError {
	if adapterErr.Provider != provider || adapterErr.validate() != nil {
		return &AdapterError{Provider: provider, Code: FailureRequest}
	}
	cloned := adapterErr
	return &cloned
}

func resultForFailure(entry adapterEntry, failure *AdapterError) ProviderResult {
	clonedFailure := *failure
	return ProviderResult{
		Provider:     entry.provider,
		Status:       statusForFailure(clonedFailure.Code),
		Dispositions: make([]Disposition, 0),
		Coverage:     cloneCoverage(entry.coverage),
		Failure:      &clonedFailure,
	}
}

func statusForFailure(code FailureCode) ResultStatus {
	if code == FailureNotConfigured {
		return ResultSkipped
	}
	return ResultFailed
}

type FailureCode string

const (
	FailureNotConfigured       FailureCode = "not_configured"
	FailureAuthentication      FailureCode = "authentication"
	FailureRateLimited         FailureCode = "rate_limited"
	FailureProviderUnavailable FailureCode = "provider_unavailable"
	FailureTimeout             FailureCode = "timeout"
	FailureInvalidResponse     FailureCode = "invalid_response"
	FailureRequest             FailureCode = "request_failed"
)

type AdapterError struct {
	Provider          Provider    `json:"provider"`
	Code              FailureCode `json:"code"`
	HTTPStatus        int         `json:"http_status,omitempty"`
	RetryAfterSeconds int64       `json:"retry_after_seconds,omitempty"`
}

func (e AdapterError) MarshalJSON() ([]byte, error) {
	if err := e.validate(); err != nil {
		return nil, errors.New("invalid adapter error cannot be marshaled")
	}
	type adapterErrorJSON AdapterError
	return json.Marshal(adapterErrorJSON(e))
}

func (e AdapterError) Error() string {
	if err := e.validateTransportError(); err != nil {
		return "provider request failed: invalid adapter error"
	}
	if e.isInternalSentinel() {
		return "provider request failed: request_failed"
	}

	message := fmt.Sprintf("provider %s request failed: %s", e.Provider, e.Code)
	switch {
	case e.HTTPStatus != 0 && e.RetryAfterSeconds != 0:
		return fmt.Sprintf("%s (http_status=%d, retry_after_seconds=%d)", message, e.HTTPStatus, e.RetryAfterSeconds)
	case e.HTTPStatus != 0:
		return fmt.Sprintf("%s (http_status=%d)", message, e.HTTPStatus)
	case e.RetryAfterSeconds != 0:
		return fmt.Sprintf("%s (retry_after_seconds=%d)", message, e.RetryAfterSeconds)
	default:
		return message
	}
}

func (e AdapterError) validate() error {
	if err := validateProvider(e.Provider); err != nil {
		return fmt.Errorf("adapter error provider must be recognized")
	}
	if !isKnownFailureCode(e.Code) {
		return fmt.Errorf("adapter error code must be recognized")
	}
	if e.HTTPStatus != 0 && (e.HTTPStatus < 100 || e.HTTPStatus > 599 || e.HTTPStatus >= 200 && e.HTTPStatus <= 299) {
		return fmt.Errorf("adapter error HTTP status must be zero or a non-success status")
	}
	if e.RetryAfterSeconds < 0 {
		return fmt.Errorf("adapter error retry_after_seconds must be nonnegative")
	}
	return nil
}

func (e AdapterError) validateTransportError() error {
	if e.isInternalSentinel() {
		return nil
	}
	return e.validate()
}

func (e AdapterError) isInternalSentinel() bool {
	return e.Provider == "" && e.Code == FailureRequest && e.HTTPStatus == 0 && e.RetryAfterSeconds == 0
}

func isKnownFailureCode(code FailureCode) bool {
	switch code {
	case FailureNotConfigured,
		FailureAuthentication,
		FailureRateLimited,
		FailureProviderUnavailable,
		FailureTimeout,
		FailureInvalidResponse,
		FailureRequest:
		return true
	default:
		return false
	}
}
