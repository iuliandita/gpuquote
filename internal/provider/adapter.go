package provider

import (
	"encoding/json"
	"errors"
	"fmt"
)

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
