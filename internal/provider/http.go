package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"
)

const maxJSONResponseBytes int64 = 8 << 20

type jsonRequest struct {
	provider          Provider
	method            string
	endpoint          string
	bearerToken       string
	body              []byte
	now               time.Time
	useRateLimitReset bool
}

func doJSON(ctx context.Context, client *http.Client, request jsonRequest, target any) *AdapterError {
	if err := validateJSONRequest(ctx, request); err != nil {
		return err
	}
	if !isValidJSONTarget(target) {
		return newAdapterError(request.provider, FailureRequest, 0, 0)
	}

	var body *bytes.Reader
	if request.method == http.MethodPost {
		body = bytes.NewReader(request.body)
	} else {
		body = bytes.NewReader(nil)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, request.method, request.endpoint, body)
	if err != nil {
		return newAdapterError(request.provider, FailureRequest, 0, 0)
	}
	httpRequest.Header.Set("Authorization", "Bearer "+request.bearerToken)
	httpRequest.Header.Set("Accept", "application/json")
	if request.method == http.MethodPost && len(request.body) != 0 {
		httpRequest.Header.Set("Content-Type", "application/json")
	}

	baseClient := client
	if baseClient == nil {
		baseClient = http.DefaultClient
	}
	clonedClient := *baseClient
	clonedClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}

	response, err := clonedClient.Do(httpRequest)
	if err != nil {
		return newAdapterError(request.provider, failureCodeForRequestError(ctx, err, FailureRequest), 0, 0)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return newAdapterError(
			request.provider,
			failureCodeForStatus(response.StatusCode),
			response.StatusCode,
			parseRetryAfter(response.Header, request.now, request.useRateLimitReset),
		)
	}

	payload, err := io.ReadAll(io.LimitReader(response.Body, maxJSONResponseBytes+1))
	if err != nil {
		return newAdapterError(request.provider, failureCodeForRequestError(ctx, err, FailureInvalidResponse), 0, 0)
	}
	if int64(len(payload)) > maxJSONResponseBytes || !decodeSingleJSONValue(payload, target) {
		return newAdapterError(request.provider, FailureInvalidResponse, 0, 0)
	}
	return nil
}

func failureCodeForRequestError(ctx context.Context, err error, fallback FailureCode) FailureCode {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return FailureTimeout
	}
	var netError net.Error
	if errors.As(err, &netError) && netError.Timeout() {
		return FailureTimeout
	}
	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		return FailureRequest
	}
	return fallback
}

func decodeSingleJSONValue(payload []byte, target any) bool {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		return false
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return false
	}

	targetValue := reflect.ValueOf(target)
	if !isValidJSONTarget(target) {
		return false
	}
	temporary := reflect.New(targetValue.Elem().Type())
	decoder = json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(temporary.Interface()); err != nil {
		return false
	}
	targetValue.Elem().Set(temporary.Elem())
	return true
}

func isValidJSONTarget(target any) bool {
	targetValue := reflect.ValueOf(target)
	return targetValue.IsValid() && targetValue.Kind() == reflect.Pointer && !targetValue.IsNil()
}

func failureCodeForStatus(status int) FailureCode {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return FailureAuthentication
	case status == http.StatusTooManyRequests:
		return FailureRateLimited
	case status >= http.StatusInternalServerError && status <= 599:
		return FailureProviderUnavailable
	default:
		return FailureRequest
	}
}

func parseRetryAfter(header http.Header, now time.Time, useRateLimitReset bool) int64 {
	retryAfter := strings.TrimSpace(header.Get("Retry-After"))
	if retryAfter != "" {
		if isDecimalDigits(retryAfter) {
			seconds, err := strconv.ParseInt(retryAfter, 10, 64)
			if err == nil && seconds > 0 {
				return seconds
			}
		}
		if retryAt, err := http.ParseTime(retryAfter); err == nil {
			if seconds := futureSeconds(now.UTC(), retryAt.UTC()); seconds > 0 {
				return seconds
			}
		}
	}

	if !useRateLimitReset {
		return 0
	}
	resetAt, err := strconv.ParseInt(strings.TrimSpace(header.Get("ratelimit-reset")), 10, 64)
	if err != nil {
		return 0
	}
	return futureSeconds(now.UTC(), time.Unix(resetAt, 0).UTC())
}

func isDecimalDigits(value string) bool {
	if value == "" {
		return false
	}
	for index := range len(value) {
		if value[index] < '0' || value[index] > '9' {
			return false
		}
	}
	return true
}

func futureSeconds(now, future time.Time) int64 {
	duration := future.Sub(now)
	if duration <= 0 {
		return 0
	}
	seconds := int64(duration / time.Second)
	if duration%time.Second != 0 {
		seconds++
	}
	return seconds
}

func validateJSONRequest(ctx context.Context, request jsonRequest) *AdapterError {
	if validateProvider(request.provider) != nil {
		return newAdapterError("", FailureRequest, 0, 0)
	}
	if ctx == nil || request.method != http.MethodGet && request.method != http.MethodPost || request.bearerToken == "" {
		return newAdapterError(request.provider, FailureRequest, 0, 0)
	}
	endpoint, err := url.Parse(request.endpoint)
	if err != nil || endpoint.Scheme != "https" || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.Opaque != "" || endpoint.Fragment != "" {
		return newAdapterError(request.provider, FailureRequest, 0, 0)
	}
	return nil
}

func newAdapterError(provider Provider, code FailureCode, status int, retryAfterSeconds int64) *AdapterError {
	if validateProvider(provider) != nil {
		return &AdapterError{Code: FailureRequest}
	}
	adapterErr := &AdapterError{
		Provider:          provider,
		Code:              code,
		HTTPStatus:        status,
		RetryAfterSeconds: retryAfterSeconds,
	}
	if err := adapterErr.validate(); err != nil {
		return &AdapterError{Provider: provider, Code: FailureRequest}
	}
	return adapterErr
}
