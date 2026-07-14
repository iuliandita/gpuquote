package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

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
