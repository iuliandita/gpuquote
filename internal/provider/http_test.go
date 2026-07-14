package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestHTTPGetRequestAndDecode(t *testing.T) {
	const token = "synthetic-bearer-token"
	type response struct {
		OK bool `json:"ok"`
	}

	var observed struct {
		method      string
		authorize   string
		accept      string
		contentType string
		body        []byte
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		observed.method = request.Method
		observed.authorize = request.Header.Get("Authorization")
		observed.accept = request.Header.Get("Accept")
		observed.contentType = request.Header.Get("Content-Type")
		observed.body, _ = io.ReadAll(request.Body)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	var target response
	request := jsonRequest{
		provider:    ProviderRunPod,
		method:      http.MethodGet,
		endpoint:    server.URL + "/offers?gpu=synthetic",
		bearerToken: token,
		body:        []byte(`{"ignored":true}`),
		now:         time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC),
	}
	if adapterErr := doJSON(context.Background(), server.Client(), request, &target); adapterErr != nil {
		t.Fatalf("doJSON() error = %v, want nil", adapterErr)
	}
	if target != (response{OK: true}) {
		t.Errorf("doJSON() target = %#v, want OK", target)
	}
	if observed.method != http.MethodGet {
		t.Errorf("method = %q, want GET", observed.method)
	}
	if observed.authorize != "Bearer "+token {
		t.Errorf("Authorization = %q, want bearer token", observed.authorize)
	}
	if observed.accept != "application/json" {
		t.Errorf("Accept = %q, want application/json", observed.accept)
	}
	if observed.contentType != "" {
		t.Errorf("Content-Type = %q, want empty", observed.contentType)
	}
	if len(observed.body) != 0 {
		t.Errorf("request body = %q, want empty", observed.body)
	}
}

func TestHTTPPostRequestAndDecode(t *testing.T) {
	const token = "synthetic-post-token"
	const requestBody = `{"gpu":"synthetic-example"}`

	var observed struct {
		method      string
		url         string
		authorize   string
		accept      string
		contentType string
		body        string
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		observed.method = request.Method
		observed.url = request.URL.String()
		observed.authorize = request.Header.Get("Authorization")
		observed.accept = request.Header.Get("Accept")
		observed.contentType = request.Header.Get("Content-Type")
		observed.body = string(body)
		_, _ = writer.Write([]byte(`{"accepted":true}`))
	}))
	defer server.Close()

	var target map[string]any
	request := jsonRequest{
		provider:    ProviderVastAI,
		method:      http.MethodPost,
		endpoint:    server.URL + "/search?market=synthetic",
		bearerToken: token,
		body:        []byte(requestBody),
		now:         time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC),
	}
	if adapterErr := doJSON(context.Background(), server.Client(), request, &target); adapterErr != nil {
		t.Fatalf("doJSON() error = %v, want nil", adapterErr)
	}
	if got, ok := target["accepted"].(bool); !ok || !got {
		t.Errorf("doJSON() target = %#v, want accepted", target)
	}
	if observed.method != http.MethodPost || observed.url != "/search?market=synthetic" {
		t.Errorf("request = %s %s, want POST /search?market=synthetic", observed.method, observed.url)
	}
	if observed.authorize != "Bearer "+token || observed.accept != "application/json" || observed.contentType != "application/json" {
		t.Errorf("headers = Authorization %q, Accept %q, Content-Type %q", observed.authorize, observed.accept, observed.contentType)
	}
	if observed.body != requestBody {
		t.Errorf("body = %q, want %q", observed.body, requestBody)
	}
	if strings.Contains(observed.url, token) || strings.Contains(observed.body, token) {
		t.Fatal("bearer token escaped the Authorization header")
	}
}

func TestHTTPPostWithoutBodyOmitsContentType(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if got := request.Header.Get("Content-Type"); got != "" {
			t.Errorf("Content-Type = %q, want empty", got)
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("reading request body: %v", err)
		}
		if len(body) != 0 {
			t.Errorf("request body = %q, want empty", body)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
			Header:     make(http.Header),
		}, nil
	})}
	request := jsonRequest{
		provider:    ProviderVastAI,
		method:      http.MethodPost,
		endpoint:    "https://catalog.example.test/search",
		bearerToken: "synthetic-empty-post-token",
		now:         time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC),
	}
	if adapterErr := doJSON(context.Background(), client, request, &map[string]any{}); adapterErr != nil {
		t.Fatalf("doJSON() error = %v, want nil", adapterErr)
	}
}

func TestHTTPRejectsInvalidInputBeforeTransport(t *testing.T) {
	type transportCounter struct {
		calls int
	}
	counter := &transportCounter{}
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		counter.calls++
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"unexpected":true}`)),
			Header:     make(http.Header),
		}, nil
	})}

	valid := jsonRequest{
		provider:    ProviderLambda,
		method:      http.MethodGet,
		endpoint:    "https://catalog.example.test/offers",
		bearerToken: "synthetic-valid-token",
		now:         time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC),
	}
	tests := []struct {
		name     string
		ctx      context.Context
		modify   func(*jsonRequest)
		rejected string
	}{
		{name: "nil context", modify: func(*jsonRequest) {}},
		{name: "PUT", ctx: context.Background(), modify: func(request *jsonRequest) { request.method = http.MethodPut }},
		{name: "DELETE", ctx: context.Background(), modify: func(request *jsonRequest) { request.method = http.MethodDelete }},
		{name: "provider", ctx: context.Background(), modify: func(request *jsonRequest) { request.provider = "synthetic-private-provider" }, rejected: "synthetic-private-provider"},
		{name: "token", ctx: context.Background(), modify: func(request *jsonRequest) { request.bearerToken = "" }},
		{name: "relative endpoint", ctx: context.Background(), modify: func(request *jsonRequest) { request.endpoint = "/private-relative-endpoint" }, rejected: "/private-relative-endpoint"},
		{name: "HTTP endpoint", ctx: context.Background(), modify: func(request *jsonRequest) { request.endpoint = "http://private.example.test/offers" }, rejected: "http://private.example.test/offers"},
		{name: "unsupported scheme", ctx: context.Background(), modify: func(request *jsonRequest) { request.endpoint = "ftp://private.example.test/offers" }, rejected: "ftp://private.example.test/offers"},
		{name: "endpoint credentials", ctx: context.Background(), modify: func(request *jsonRequest) {
			request.endpoint = "https://private-user:private-password@example.test/offers"
		}, rejected: "private-password"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := valid
			test.modify(&request)
			target := map[string]any{"sentinel": true}
			before := counter.calls
			adapterErr := doJSON(test.ctx, client, request, &target)
			if adapterErr == nil || adapterErr.Code != FailureRequest {
				t.Fatalf("doJSON() error = %#v, want request failure", adapterErr)
			}
			if counter.calls != before {
				t.Errorf("transport calls = %d, want %d", counter.calls, before)
			}
			if got, ok := target["sentinel"].(bool); !ok || !got || len(target) != 1 {
				t.Errorf("target mutated to %#v", target)
			}
			if test.rejected != "" {
				serialized := ""
				if adapterErr.Provider == "" {
					encoded, err := json.Marshal(adapterErr)
					if err == nil {
						t.Fatalf("json.Marshal(sentinel AdapterError) = %s, nil; want error", encoded)
					}
					serialized = string(encoded) + err.Error()
				} else {
					serialized = marshalAdapterErrorForTest(t, adapterErr)
				}
				if strings.Contains(adapterErr.Error(), test.rejected) || strings.Contains(serialized, test.rejected) {
					t.Fatalf("error leaked rejected input %q", test.rejected)
				}
			}
			if test.name == "provider" && adapterErr.Provider != "" {
				t.Errorf("invalid provider failure retained provider %q, want sanitized zero value", adapterErr.Provider)
			}
		})
	}
}

func TestHTTPRejectsInvalidTargetBeforeTransport(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"unexpected":true}`)),
			Header:     make(http.Header),
		}, nil
	})}
	request := jsonRequest{
		provider:    ProviderLambda,
		method:      http.MethodGet,
		endpoint:    "https://catalog.example.test/offers",
		bearerToken: "synthetic-target-token",
		now:         time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC),
	}
	var typedNil *map[string]any
	tests := []struct {
		name   string
		target any
	}{
		{name: "nil"},
		{name: "typed nil pointer", target: typedNil},
		{name: "non-pointer", target: map[string]any{}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapterErr := doJSON(context.Background(), client, request, test.target)
			if adapterErr == nil || adapterErr.Code != FailureRequest {
				t.Fatalf("doJSON() error = %#v, want request failure", adapterErr)
			}
			if calls != 0 {
				t.Errorf("transport calls = %d, want zero", calls)
			}
		})
	}
}

func TestHTTPUsesClonedClientAndDeniesRedirects(t *testing.T) {
	redirectTargetCalls := 0
	target := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		redirectTargetCalls++
		_, _ = writer.Write([]byte(`{"followed":true}`))
	}))
	defer target.Close()

	source := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL+"/private-target", http.StatusFound)
	}))
	defer source.Close()

	callerRedirectCalls := 0
	callerRedirect := func(*http.Request, []*http.Request) error {
		callerRedirectCalls++
		return nil
	}
	transport := source.Client().Transport
	timeout := 17 * time.Second
	client := &http.Client{Transport: transport, Timeout: timeout, CheckRedirect: callerRedirect}
	request := jsonRequest{
		provider:    ProviderDigitalOcean,
		method:      http.MethodGet,
		endpoint:    source.URL + "/redirect",
		bearerToken: "synthetic-redirect-token",
		now:         time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC),
	}
	adapterErr := doJSON(context.Background(), client, request, &map[string]any{})
	if adapterErr == nil || adapterErr.Code != FailureRequest || adapterErr.HTTPStatus != http.StatusFound {
		t.Fatalf("doJSON() error = %#v, want request failure with status 302", adapterErr)
	}
	if redirectTargetCalls != 0 || callerRedirectCalls != 0 {
		t.Errorf("redirect target calls = %d, caller CheckRedirect calls = %d; want zero", redirectTargetCalls, callerRedirectCalls)
	}
	if client.Transport != transport || client.Timeout != timeout || client.CheckRedirect == nil || reflect.ValueOf(client.CheckRedirect).Pointer() != reflect.ValueOf(callerRedirect).Pointer() {
		t.Error("doJSON() mutated caller client")
	}
}

func TestHTTPAcceptsNilClient(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := jsonRequest{
		provider:    ProviderRunPod,
		method:      http.MethodGet,
		endpoint:    "https://catalog.example.test/offers",
		bearerToken: "synthetic-default-client-token",
		now:         time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC),
	}
	if adapterErr := doJSON(ctx, nil, request, &map[string]any{}); adapterErr == nil || adapterErr.Code != FailureRequest {
		t.Fatalf("doJSON() error = %#v, want canceled request failure", adapterErr)
	}
}

func TestHTTPMapsStatusWithoutRetrying(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		wantCode FailureCode
	}{
		{name: "redirect", status: http.StatusTemporaryRedirect, wantCode: FailureRequest},
		{name: "bad request", status: http.StatusBadRequest, wantCode: FailureRequest},
		{name: "unauthorized", status: http.StatusUnauthorized, wantCode: FailureAuthentication},
		{name: "forbidden", status: http.StatusForbidden, wantCode: FailureAuthentication},
		{name: "not found", status: http.StatusNotFound, wantCode: FailureRequest},
		{name: "rate limited", status: http.StatusTooManyRequests, wantCode: FailureRateLimited},
		{name: "internal error", status: http.StatusInternalServerError, wantCode: FailureProviderUnavailable},
		{name: "unavailable", status: http.StatusServiceUnavailable, wantCode: FailureProviderUnavailable},
		{name: "last server error", status: 599, wantCode: FailureProviderUnavailable},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{
					StatusCode: test.status,
					Body:       io.NopCloser(strings.NewReader("synthetic-private-response-body")),
					Header:     make(http.Header),
				}, nil
			})}
			request := jsonRequest{
				provider:    ProviderLambda,
				method:      http.MethodGet,
				endpoint:    "https://catalog.example.test/offers",
				bearerToken: "synthetic-status-token",
				now:         time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC),
			}
			adapterErr := doJSON(context.Background(), client, request, &map[string]any{})
			if adapterErr == nil || adapterErr.Code != test.wantCode || adapterErr.HTTPStatus != test.status {
				t.Fatalf("doJSON() error = %#v, want code %q and status %d", adapterErr, test.wantCode, test.status)
			}
			if calls != 1 {
				t.Errorf("transport calls = %d, want 1", calls)
			}
			if strings.Contains(adapterErr.Error(), "synthetic-private-response-body") || strings.Contains(marshalAdapterErrorForTest(t, adapterErr), "synthetic-private-response-body") {
				t.Fatal("status error leaked response body")
			}
		})
	}
}

func TestHTTPReturnsOnlyTransportValidatedAdapterErrors(t *testing.T) {
	request := jsonRequest{
		provider:    "synthetic-private-provider",
		method:      http.MethodGet,
		endpoint:    "https://catalog.example.test/offers",
		bearerToken: "synthetic-validation-token",
		now:         time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC),
	}
	invalidProviderErr := doJSON(context.Background(), nil, request, &map[string]any{})

	request.provider = ProviderLambda
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 600,
			Body:       io.NopCloser(strings.NewReader("synthetic-private-status-body")),
			Header:     make(http.Header),
		}, nil
	})}
	invalidStatusErr := doJSON(context.Background(), client, request, &map[string]any{})

	tests := []struct {
		name        string
		adapterErr  *AdapterError
		want        AdapterError
		strictValid bool
	}{
		{name: "invalid provider", adapterErr: invalidProviderErr, want: AdapterError{Code: FailureRequest}},
		{name: "invalid status", adapterErr: invalidStatusErr, want: AdapterError{Provider: ProviderLambda, Code: FailureRequest}, strictValid: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.adapterErr == nil || *test.adapterErr != test.want {
				t.Fatalf("doJSON() error = %#v, want %#v", test.adapterErr, test.want)
			}
			if err := test.adapterErr.validateTransportError(); err != nil {
				t.Errorf("doJSON() transport error validation = %v, want nil", err)
			}
			strictErr := test.adapterErr.validate()
			if test.strictValid && strictErr != nil {
				t.Errorf("recognized doJSON() error validation = %v, want nil", strictErr)
			}
			if !test.strictValid && strictErr == nil {
				t.Error("sentinel doJSON() error validation = nil, want non-nil")
			}
		})
	}
}

func TestHTTPParsesRetryHeaders(t *testing.T) {
	now := time.Date(2026, time.July, 14, 12, 0, 0, 0, time.FixedZone("synthetic-offset", 2*60*60))
	nowUTC := now.UTC()
	tests := []struct {
		name              string
		retryAfter        string
		rateLimitReset    string
		useRateLimitReset bool
		wantSeconds       int64
	}{
		{name: "integer seconds", retryAfter: "45", wantSeconds: 45},
		{name: "HTTP date", retryAfter: nowUTC.Add(90 * time.Second).Format(http.TimeFormat), wantSeconds: 90},
		{name: "valid Retry-After preferred", retryAfter: "30", rateLimitReset: "1784023560", useRateLimitReset: true, wantSeconds: 30},
		{name: "reset fallback after invalid Retry-After", retryAfter: "invalid", rateLimitReset: "1784023260", useRateLimitReset: true, wantSeconds: 60},
		{name: "reset fallback after past Retry-After", retryAfter: nowUTC.Add(-time.Second).Format(http.TimeFormat), rateLimitReset: "1784023260", useRateLimitReset: true, wantSeconds: 60},
		{name: "reset disabled", retryAfter: "invalid", rateLimitReset: "1784023260", wantSeconds: 0},
		{name: "negative seconds", retryAfter: "-1", wantSeconds: 0},
		{name: "zero seconds", retryAfter: "0", wantSeconds: 0},
		{name: "past HTTP date", retryAfter: nowUTC.Add(-time.Second).Format(http.TimeFormat), wantSeconds: 0},
		{name: "past reset", rateLimitReset: "1784023199", useRateLimitReset: true, wantSeconds: 0},
		{name: "invalid reset", rateLimitReset: "invalid", useRateLimitReset: true, wantSeconds: 0},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			header := make(http.Header)
			if test.retryAfter != "" {
				header.Set("Retry-After", test.retryAfter)
			}
			if test.rateLimitReset != "" {
				header.Set("ratelimit-reset", test.rateLimitReset)
			}
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusTooManyRequests,
					Body:       io.NopCloser(strings.NewReader(`{"ignored":true}`)),
					Header:     header,
				}, nil
			})}
			request := jsonRequest{
				provider:          ProviderVastAI,
				method:            http.MethodGet,
				endpoint:          "https://catalog.example.test/offers",
				bearerToken:       "synthetic-retry-token",
				now:               now,
				useRateLimitReset: test.useRateLimitReset,
			}
			adapterErr := doJSON(context.Background(), client, request, &map[string]any{})
			if adapterErr == nil || adapterErr.Code != FailureRateLimited || adapterErr.RetryAfterSeconds != test.wantSeconds {
				t.Fatalf("doJSON() error = %#v, want rate limit with retry %d", adapterErr, test.wantSeconds)
			}
		})
	}
}

func TestHTTPDecodesExactlyOneJSONValueWithNumbersPreserved(t *testing.T) {
	tests := []struct {
		name         string
		body         string
		wantErr      bool
		wantNumber   string
		wantSentinel bool
	}{
		{name: "number", body: `{"number":9007199254740993.25}`, wantNumber: "9007199254740993.25"},
		{name: "trailing whitespace", body: "{\"number\":1}\n\t ", wantNumber: "1"},
		{name: "empty", body: "", wantErr: true, wantSentinel: true},
		{name: "malformed", body: `{"private":"synthetic-private-response-body"`, wantErr: true, wantSentinel: true},
		{name: "second value", body: `{"private":"synthetic-private-response-body"}{}`, wantErr: true, wantSentinel: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(test.body)),
					Header:     make(http.Header),
				}, nil
			})}
			request := jsonRequest{
				provider:    ProviderRunPod,
				method:      http.MethodGet,
				endpoint:    "https://catalog.example.test/offers",
				bearerToken: "synthetic-json-token",
				now:         time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC),
			}
			target := map[string]any{"sentinel": true}
			adapterErr := doJSON(context.Background(), client, request, &target)
			if test.wantErr {
				if adapterErr == nil || adapterErr.Code != FailureInvalidResponse {
					t.Fatalf("doJSON() error = %#v, want invalid response", adapterErr)
				}
				if test.wantSentinel {
					if got, ok := target["sentinel"].(bool); !ok || !got || len(target) != 1 {
						t.Errorf("invalid response mutated target to %#v", target)
					}
				}
				if strings.Contains(adapterErr.Error(), "synthetic-private-response-body") || strings.Contains(marshalAdapterErrorForTest(t, adapterErr), "synthetic-private-response-body") {
					t.Fatal("invalid response error leaked body text")
				}
				return
			}
			if adapterErr != nil {
				t.Fatalf("doJSON() error = %v, want nil", adapterErr)
			}
			number, ok := target["number"].(json.Number)
			if !ok || number.String() != test.wantNumber {
				t.Errorf("decoded number = %#v, want json.Number(%q)", target["number"], test.wantNumber)
			}
		})
	}
}

func TestHTTPBoundsResponseBodyAtEightMiB(t *testing.T) {
	const responseLimit = 8 << 20
	makeBody := func(size int) string {
		const prefix = `{"data":"`
		const suffix = `"}`
		return prefix + strings.Repeat("a", size-len(prefix)-len(suffix)) + suffix
	}

	tests := []struct {
		name    string
		size    int
		wantErr bool
	}{
		{name: "exact limit", size: responseLimit},
		{name: "one byte over", size: responseLimit + 1, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := makeBody(test.size)
			if len(body) != test.size {
				t.Fatalf("fixture size = %d, want %d", len(body), test.size)
			}
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(body)),
					Header:     make(http.Header),
				}, nil
			})}
			request := jsonRequest{
				provider:    ProviderLambda,
				method:      http.MethodGet,
				endpoint:    "https://catalog.example.test/offers",
				bearerToken: "synthetic-bound-token",
				now:         time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC),
			}
			var target struct {
				Data string `json:"data"`
			}
			adapterErr := doJSON(context.Background(), client, request, &target)
			if test.wantErr {
				if adapterErr == nil || adapterErr.Code != FailureInvalidResponse {
					t.Fatalf("doJSON() error = %#v, want invalid response", adapterErr)
				}
				if target.Data != "" {
					t.Error("oversized response mutated target")
				}
				return
			}
			if adapterErr != nil {
				t.Fatalf("doJSON() error = %v, want nil", adapterErr)
			}
			if len(target.Data) != responseLimit-len(`{"data":"`)-len(`"}`) {
				t.Errorf("decoded data length = %d, want bounded payload", len(target.Data))
			}
		})
	}
}

func TestHTTPClassifiesTransportAndContextErrors(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode FailureCode
	}{
		{name: "deadline", err: context.DeadlineExceeded, wantCode: FailureTimeout},
		{name: "net timeout", err: syntheticTimeoutError{}, wantCode: FailureTimeout},
		{name: "canceled", err: context.Canceled, wantCode: FailureRequest},
		{name: "transport", err: errors.New("synthetic-private-transport-text"), wantCode: FailureRequest},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return nil, test.err
			})}
			request := jsonRequest{
				provider:    ProviderDigitalOcean,
				method:      http.MethodGet,
				endpoint:    "https://catalog.example.test/offers",
				bearerToken: "synthetic-transport-token",
				now:         time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC),
			}
			target := map[string]any{"sentinel": true}
			adapterErr := doJSON(context.Background(), client, request, &target)
			if adapterErr == nil || adapterErr.Code != test.wantCode {
				t.Fatalf("doJSON() error = %#v, want code %q", adapterErr, test.wantCode)
			}
			if calls != 1 {
				t.Errorf("transport calls = %d, want 1", calls)
			}
			for _, secret := range []string{"synthetic-private-transport-text", "synthetic-transport-token"} {
				if strings.Contains(adapterErr.Error(), secret) || strings.Contains(marshalAdapterErrorForTest(t, adapterErr), secret) {
					t.Fatalf("transport error leaked %q", secret)
				}
			}
			if got, ok := target["sentinel"].(bool); !ok || !got || len(target) != 1 {
				t.Errorf("transport failure mutated target to %#v", target)
			}
		})
	}
}

func TestHTTPClassifiesBodyReadContextInterruption(t *testing.T) {
	tests := []struct {
		name       string
		contextFor func() (context.Context, context.CancelFunc)
		cancel     bool
		wantCode   FailureCode
	}{
		{
			name: "canceled",
			contextFor: func() (context.Context, context.CancelFunc) {
				return context.WithCancel(context.Background())
			},
			cancel:   true,
			wantCode: FailureRequest,
		},
		{
			name: "deadline",
			contextFor: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 500*time.Millisecond)
			},
			wantCode: FailureTimeout,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requestStopped := make(chan struct{}, 1)
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				writer.WriteHeader(http.StatusOK)
				flusher, ok := writer.(http.Flusher)
				if !ok {
					t.Error("response writer does not support flushing")
					return
				}
				flusher.Flush()
				select {
				case <-request.Context().Done():
					requestStopped <- struct{}{}
				case <-time.After(2 * time.Second):
				}
			}))
			defer server.Close()

			responseReady := make(chan struct{}, 1)
			transport := server.Client().Transport
			client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				response, err := transport.RoundTrip(request)
				if err == nil {
					responseReady <- struct{}{}
				}
				return response, err
			})}
			ctx, cancel := test.contextFor()
			defer cancel()
			request := jsonRequest{
				provider:    ProviderRunPod,
				method:      http.MethodGet,
				endpoint:    server.URL,
				bearerToken: "synthetic-body-context-token",
				now:         time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC),
			}
			target := map[string]any{"sentinel": true}
			result := make(chan *AdapterError, 1)
			go func() {
				result <- doJSON(ctx, client, request, &target)
			}()

			select {
			case <-responseReady:
			case <-time.After(2 * time.Second):
				t.Fatal("client did not receive flushed response headers")
			}
			if test.cancel {
				cancel()
			}
			select {
			case adapterErr := <-result:
				if adapterErr == nil || adapterErr.Code != test.wantCode {
					t.Fatalf("doJSON() error = %#v, want code %q", adapterErr, test.wantCode)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("body read did not stop after context completion")
			}
			select {
			case <-requestStopped:
			case <-time.After(2 * time.Second):
				t.Fatal("server request context was not canceled")
			}
			if got, ok := target["sentinel"].(bool); !ok || !got || len(target) != 1 {
				t.Errorf("body read interruption mutated target to %#v", target)
			}
		})
	}
}

func TestHTTPClassifiesBodyReadNetTimeout(t *testing.T) {
	body := &trackingBody{Reader: timeoutReader{}}
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: body, Header: make(http.Header)}, nil
	})}
	request := jsonRequest{
		provider:    ProviderVastAI,
		method:      http.MethodGet,
		endpoint:    "https://catalog.example.test/offers",
		bearerToken: "synthetic-body-timeout-token",
		now:         time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC),
	}
	target := map[string]any{"sentinel": true}
	adapterErr := doJSON(context.Background(), client, request, &target)
	if adapterErr == nil || adapterErr.Code != FailureTimeout {
		t.Fatalf("doJSON() error = %#v, want timeout", adapterErr)
	}
	if !body.closed {
		t.Error("response body was not closed after timeout")
	}
	if got, ok := target["sentinel"].(bool); !ok || !got || len(target) != 1 {
		t.Errorf("body read timeout mutated target to %#v", target)
	}
	for _, secret := range []string{"synthetic-private-timeout-text", "synthetic-body-timeout-token"} {
		if strings.Contains(adapterErr.Error(), secret) || strings.Contains(marshalAdapterErrorForTest(t, adapterErr), secret) {
			t.Fatalf("body read timeout leaked %q", secret)
		}
	}
}

func TestHTTPCancellationStopsBlockingRequest(t *testing.T) {
	tests := []struct {
		name       string
		contextFor func() (context.Context, context.CancelFunc)
		wantCode   FailureCode
	}{
		{
			name: "canceled",
			contextFor: func() (context.Context, context.CancelFunc) {
				return context.WithCancel(context.Background())
			},
			wantCode: FailureRequest,
		},
		{
			name: "deadline",
			contextFor: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 50*time.Millisecond)
			},
			wantCode: FailureTimeout,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			started := make(chan struct{}, 1)
			requestStopped := make(chan struct{}, 1)
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				started <- struct{}{}
				select {
				case <-request.Context().Done():
					requestStopped <- struct{}{}
				case <-time.After(2 * time.Second):
				}
			}))
			defer server.Close()

			ctx, cancel := test.contextFor()
			defer cancel()
			request := jsonRequest{
				provider:    ProviderRunPod,
				method:      http.MethodGet,
				endpoint:    server.URL,
				bearerToken: "synthetic-cancel-token",
				now:         time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC),
			}
			result := make(chan *AdapterError, 1)
			go func() {
				result <- doJSON(ctx, server.Client(), request, &map[string]any{})
			}()

			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("server did not receive request")
			}
			if test.name == "canceled" {
				cancel()
			}
			select {
			case adapterErr := <-result:
				if adapterErr == nil || adapterErr.Code != test.wantCode {
					t.Fatalf("doJSON() error = %#v, want code %q", adapterErr, test.wantCode)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("doJSON() did not stop after context completion")
			}
			select {
			case <-requestStopped:
			case <-time.After(2 * time.Second):
				t.Fatal("server request context was not canceled")
			}
		})
	}
}

func TestHTTPClosesResponseBodiesOnEveryPath(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{name: "success", status: http.StatusOK, body: `{"ok":true}`},
		{name: "invalid JSON", status: http.StatusOK, body: `{"private":"synthetic-private-response-body"`},
		{name: "status failure", status: http.StatusServiceUnavailable, body: "synthetic-private-response-body"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := &trackingBody{Reader: strings.NewReader(test.body)}
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: test.status, Body: body, Header: make(http.Header)}, nil
			})}
			request := jsonRequest{
				provider:    ProviderVastAI,
				method:      http.MethodGet,
				endpoint:    "https://catalog.example.test/offers",
				bearerToken: "synthetic-close-token",
				now:         time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC),
			}
			_ = doJSON(context.Background(), client, request, &map[string]any{})
			if !body.closed {
				t.Error("response body was not closed")
			}
		})
	}

	t.Run("read failure", func(t *testing.T) {
		body := &trackingBody{Reader: errorReader{}}
		client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: body, Header: make(http.Header)}, nil
		})}
		request := jsonRequest{
			provider:    ProviderVastAI,
			method:      http.MethodGet,
			endpoint:    "https://catalog.example.test/offers",
			bearerToken: "synthetic-close-token",
			now:         time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC),
		}
		adapterErr := doJSON(context.Background(), client, request, &map[string]any{})
		if adapterErr == nil || adapterErr.Code != FailureInvalidResponse {
			t.Fatalf("doJSON() error = %#v, want invalid response", adapterErr)
		}
		if !body.closed {
			t.Error("response body was not closed after read failure")
		}
		if strings.Contains(adapterErr.Error(), "synthetic-private-read-text") || strings.Contains(marshalAdapterErrorForTest(t, adapterErr), "synthetic-private-read-text") {
			t.Fatal("read failure leaked body error text")
		}
	})
}

func TestHTTPNeverLeaksTokenOrFailureText(t *testing.T) {
	const token = "synthetic-private-token"
	const transportText = "synthetic-private-transport-text"
	observedURL := ""
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		observedURL = request.URL.String()
		return nil, errors.New(transportText)
	})}
	request := jsonRequest{
		provider:    ProviderLambda,
		method:      http.MethodPost,
		endpoint:    "https://catalog.example.test/offers?market=synthetic",
		bearerToken: token,
		body:        []byte(`{"query":"synthetic"}`),
		now:         time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC),
	}
	target := map[string]any{"sentinel": true}
	adapterErr := doJSON(context.Background(), client, request, &target)
	if adapterErr == nil || adapterErr.Code != FailureRequest {
		t.Fatalf("doJSON() error = %#v, want request failure", adapterErr)
	}
	returnedTarget, err := json.Marshal(target)
	if err != nil {
		t.Fatalf("json.Marshal(target) error = %v", err)
	}
	outputs := []string{adapterErr.Error(), marshalAdapterErrorForTest(t, adapterErr), observedURL, string(returnedTarget)}
	for _, output := range outputs {
		for _, secret := range []string{token, transportText} {
			if strings.Contains(output, secret) {
				t.Fatalf("output %q leaked %q", output, secret)
			}
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

type syntheticTimeoutError struct{}

func (syntheticTimeoutError) Error() string   { return "synthetic-private-timeout-text" }
func (syntheticTimeoutError) Timeout() bool   { return true }
func (syntheticTimeoutError) Temporary() bool { return true }

type trackingBody struct {
	io.Reader
	closed bool
}

func (body *trackingBody) Close() error {
	body.closed = true
	return nil
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) {
	return 0, errors.New("synthetic-private-read-text")
}

type timeoutReader struct{}

func (timeoutReader) Read([]byte) (int, error) {
	return 0, syntheticTimeoutError{}
}

func marshalAdapterErrorForTest(t *testing.T, adapterErr *AdapterError) string {
	t.Helper()
	encoded, err := json.Marshal(adapterErr)
	if err != nil {
		t.Fatalf("json.Marshal(AdapterError) error = %v", err)
	}
	return string(encoded)
}
