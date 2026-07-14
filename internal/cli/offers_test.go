package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iuliandita/gpuquote/internal/provider"
)

const offersTestCredential = "synthetic-private-credential"

var offersTestNow = time.Date(2026, time.July, 14, 18, 30, 0, 0, time.UTC)

type offersFakeAdapter struct {
	providerName provider.Provider
	coverage     provider.Coverage
	discover     func(context.Context) (provider.Snapshot, error)
}

type offersRoundTripFunc func(*http.Request) (*http.Response, error)

func (f offersRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type offersShortWriter struct{}

func (offersShortWriter) Write(value []byte) (int, error) {
	return len(value) - 1, nil
}

func (a *offersFakeAdapter) Provider() provider.Provider { return a.providerName }
func (a *offersFakeAdapter) Coverage() provider.Coverage { return a.coverage }
func (a *offersFakeAdapter) Discover(ctx context.Context) (provider.Snapshot, error) {
	return a.discover(ctx)
}

func TestREADMEOffersZshWorkflowStartsWithInstall(t *testing.T) {
	contents, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	const want = "```zsh\ngo install ./cmd/gpuquote\nread -rs \"DIGITALOCEAN_TOKEN?DigitalOcean token: \""
	if !strings.Contains(string(contents), want) {
		t.Fatalf("README offers zsh workflow does not start with %q", "go install ./cmd/gpuquote")
	}
}

func TestRunOffersAllMissingCredentialsPrintsSafeGuidance(t *testing.T) {
	for _, name := range []string{"RUNPOD_API_KEY", "VAST_API_KEY", "LAMBDA_API_KEY", "DIGITALOCEAN_TOKEN"} {
		t.Setenv(name, "")
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run([]string{"offers"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("code = %d, want 2; stdout = %q; stderr = %q", code, stdout.String(), stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	const wantStderr = "offers: no provider credentials configured; set RUNPOD_API_KEY, VAST_API_KEY, LAMBDA_API_KEY, or DIGITALOCEAN_TOKEN\n"
	if stderr.String() != wantStderr {
		t.Fatalf("stderr = %q, want %q", stderr.String(), wantStderr)
	}
}

func TestRunOffersSelectedMissingCredentialMakesNoNetworkRequest(t *testing.T) {
	var requests atomic.Int32
	client := &http.Client{Transport: offersRoundTripFunc(func(*http.Request) (*http.Response, error) {
		requests.Add(1)
		return nil, errors.New("unexpected network request")
	})}
	deps := dependencies{
		getenv:      func(string) string { return "" },
		httpClient:  client,
		now:         func() time.Time { return offersTestNow },
		newAdapters: newProviderAdapters,
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"offers", "-provider", "runpod", "-output", "check"}, &stdout, &stderr, deps)
	if code != 2 || requests.Load() != 0 || stdout.Len() != 0 {
		t.Fatalf("code = %d requests = %d; stdout = %q; stderr = %q", code, requests.Load(), stdout.String(), stderr.String())
	}
	const wantStderr = "offers: no provider credentials configured; set RUNPOD_API_KEY, VAST_API_KEY, LAMBDA_API_KEY, or DIGITALOCEAN_TOKEN\n"
	if stderr.String() != wantStderr {
		t.Fatalf("stderr = %q, want %q", stderr.String(), wantStderr)
	}
}

func TestRunOffersJSONIsStableCompleteAndSafe(t *testing.T) {
	rawBody := "synthetic-private-response-body"
	runPodAccepted := mustOffersAccepted(t, provider.ProviderRunPod, "offer-z", `Synthetic "GPU"`, "9007199254740993.125")
	runPodRejected := mustOffersRejected(t, provider.ProviderRunPod, "offer-m", `Synthetic "reason"`)
	runPodFirst := mustOffersAccepted(t, provider.ProviderRunPod, "offer-a", "Synthetic GPU A", "0.125")
	vastRejected := mustOffersRejected(t, provider.ProviderVastAI, "vast-partial", "Synthetic partial")

	adapters := []provider.Adapter{
		offersSkippedAdapter(provider.ProviderDigitalOcean),
		offersFailedAdapter(provider.ProviderLambda, errors.New(rawBody)),
		offersPartialAdapter(provider.ProviderVastAI, []provider.Disposition{vastRejected}, provider.FailureRateLimited),
		offersSuccessAdapter(provider.ProviderRunPod, []provider.Disposition{runPodAccepted, runPodRejected, runPodFirst}, []provider.Reason{{Code: provider.ReasonCoverageLimited, Field: "catalog", Message: "Synthetic <warning>"}}),
	}
	credentials := map[string]string{
		"RUNPOD_API_KEY":     offersTestCredential + "-runpod",
		"VAST_API_KEY":       offersTestCredential + "-vast",
		"LAMBDA_API_KEY":     offersTestCredential + "-lambda",
		"DIGITALOCEAN_TOKEN": offersTestCredential + "-digitalocean",
	}
	client := &http.Client{}
	var clockCalls atomic.Int32
	deps := dependencies{
		getenv:     func(name string) string { return credentials[name] },
		httpClient: client,
		now: func() time.Time {
			clockCalls.Add(1)
			return offersTestNow
		},
		newAdapters: func(selection string, gotCredentials map[provider.Provider]string, gotClient *http.Client, now func() time.Time) ([]provider.Adapter, error) {
			if selection != "all" {
				t.Errorf("selection = %q, want all", selection)
			}
			if gotClient != client {
				t.Error("adapter factory did not receive the shared HTTP client")
			}
			if now().Equal(offersTestNow) == false {
				t.Errorf("adapter clock = %v, want %v", now(), offersTestNow)
			}
			want := map[provider.Provider]string{
				provider.ProviderRunPod:       credentials["RUNPOD_API_KEY"],
				provider.ProviderVastAI:       credentials["VAST_API_KEY"],
				provider.ProviderLambda:       credentials["LAMBDA_API_KEY"],
				provider.ProviderDigitalOcean: credentials["DIGITALOCEAN_TOKEN"],
			}
			if fmt.Sprint(gotCredentials) != fmt.Sprint(want) {
				t.Errorf("credentials keys = %#v, want exact provider mapping", gotCredentials)
			}
			return adapters, nil
		},
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"offers", "-output", "json"}, &stdout, &stderr, deps)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stdout = %q; stderr = %q", code, stdout.String(), stderr.String())
	}
	if clockCalls.Load() != 1 {
		t.Fatalf("clock calls = %d, want exactly 1", clockCalls.Load())
	}
	if !strings.HasSuffix(stdout.String(), "\n") {
		t.Fatalf("JSON output lacks trailing newline: %q", stdout.String())
	}
	if strings.Contains(stdout.String(), "<warning>") || !strings.Contains(stdout.String(), `\u003cwarning\u003e`) {
		t.Fatalf("JSON output did not enable HTML escaping: %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), `"quoted_usd_per_hour":"9007199254740993.125"`) {
		t.Fatalf("JSON output changed exact decimal string: %s", stdout.String())
	}
	for _, secret := range append([]string{rawBody}, mapValues(credentials)...) {
		if strings.Contains(stdout.String(), secret) || strings.Contains(stderr.String(), secret) {
			t.Fatalf("output leaked %q; stdout = %q; stderr = %q", secret, stdout.String(), stderr.String())
		}
	}

	var output struct {
		SchemaVersion int                       `json:"schema_version"`
		CollectedAt   time.Time                 `json:"collected_at"`
		Providers     []provider.ProviderResult `json:"providers"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	if output.SchemaVersion != 1 || !output.CollectedAt.Equal(offersTestNow) || output.CollectedAt.Location() != time.UTC {
		t.Fatalf("envelope = %#v", output)
	}
	wantProviders := []provider.Provider{provider.ProviderRunPod, provider.ProviderVastAI, provider.ProviderLambda, provider.ProviderDigitalOcean}
	if len(output.Providers) != len(wantProviders) {
		t.Fatalf("providers = %#v", output.Providers)
	}
	for index, want := range wantProviders {
		if output.Providers[index].Provider != want {
			t.Fatalf("provider[%d] = %q, want %q", index, output.Providers[index].Provider, want)
		}
	}
	if got := offersDispositionIDs(output.Providers[0].Dispositions); fmt.Sprint(got) != "[offer-a offer-m offer-z]" {
		t.Fatalf("RunPod disposition order = %v", got)
	}
	if output.Providers[0].Status != provider.ResultSuccess || len(output.Providers[0].Warnings) != 1 {
		t.Fatalf("RunPod result = %#v", output.Providers[0])
	}
	if output.Providers[1].Status != provider.ResultPartial || output.Providers[1].Failure == nil || output.Providers[1].Failure.Code != provider.FailureRateLimited || len(output.Providers[1].Dispositions) != 1 {
		t.Fatalf("Vast result = %#v", output.Providers[1])
	}
	if output.Providers[2].Status != provider.ResultFailed || output.Providers[2].Failure == nil || output.Providers[2].Failure.Code != provider.FailureRequest {
		t.Fatalf("Lambda result = %#v", output.Providers[2])
	}
	if output.Providers[3].Status != provider.ResultSkipped || output.Providers[3].Failure == nil || output.Providers[3].Failure.Code != provider.FailureNotConfigured {
		t.Fatalf("DigitalOcean result = %#v", output.Providers[3])
	}
}

func TestRunOffersTextQuotesProviderControlledStrings(t *testing.T) {
	rawBody := "synthetic-private-text-body"
	accepted := mustOffersAccepted(t, provider.ProviderRunPod, "offer-a", `Synthetic "GPU"`, "9007199254740993.125")
	rejected := mustOffersRejected(t, provider.ProviderRunPod, "offer-z", `Synthetic "reason"`)
	adapters := []provider.Adapter{
		offersFailedAdapter(provider.ProviderLambda, errors.New(rawBody)),
		offersSuccessAdapter(provider.ProviderRunPod, []provider.Disposition{rejected, accepted}, []provider.Reason{{Code: provider.ReasonCoverageLimited, Field: "catalog", Message: "Synthetic warning"}}),
	}
	deps := configuredOffersDependencies(adapters)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"offers"}, &stdout, &stderr, deps)
	if code != 0 {
		t.Fatalf("code = %d; stderr = %q", code, stderr.String())
	}
	const want = "provider=runpod status=success coverage=complete market=\"synthetic-runpod\" dispositions=2 warnings=1\n" +
		"accepted source_id=\"offer-a\" gpu_model=\"Synthetic \\\"GPU\\\"\" gpu_count=1 region=\"<region&>\" country=\"US\" price_usd_per_hour=\"9007199254740993.125\" rate_scope=\"total\"\n" +
		"rejected source_id=\"offer-z\" reason=\"invalid_value\" field=\"gpu_model\" message=\"Synthetic \\\"reason\\\"\"\n" +
		"provider=lambda status=failed coverage=incomplete market=\"synthetic-lambda\" dispositions=0 warnings=0 failure=\"request_failed\"\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
	if strings.Contains(stdout.String(), rawBody) || strings.Contains(stderr.String(), rawBody) || strings.Contains(stdout.String(), offersTestCredential) {
		t.Fatalf("text output leaked private input; stdout = %q; stderr = %q", stdout.String(), stderr.String())
	}
}

func TestRunOffersCheckPrintsOnlyFixedFields(t *testing.T) {
	accepted := mustOffersAccepted(t, provider.ProviderRunPod, "synthetic-secret-offer", "Synthetic secret GPU", "123456789.125")
	adapters := []provider.Adapter{
		offersSkippedAdapter(provider.ProviderDigitalOcean),
		offersFailedAdapter(provider.ProviderLambda, &provider.AdapterError{Provider: provider.ProviderLambda, Code: provider.FailureAuthentication, HTTPStatus: http.StatusUnauthorized}),
		offersTruncatedAdapter(provider.ProviderVastAI),
		offersSuccessAdapter(provider.ProviderRunPod, []provider.Disposition{accepted}, []provider.Reason{{Code: provider.ReasonCoverageLimited, Field: "synthetic-secret-field", Message: "synthetic-secret-warning"}}),
	}
	deps := configuredOffersDependencies(adapters)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"offers", "-output", "check"}, &stdout, &stderr, deps)
	if code != 0 {
		t.Fatalf("code = %d; stderr = %q", code, stderr.String())
	}
	const want = "provider=runpod status=success coverage=complete\n" +
		"provider=vastai status=partial coverage=truncated\n" +
		"provider=lambda status=failed coverage=incomplete failure=authentication\n" +
		"provider=digitalocean status=skipped coverage=incomplete failure=not_configured\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
	for _, forbidden := range []string{"synthetic-secret", "123456789.125", "disposition", "warning", "price", "body", offersTestCredential} {
		if strings.Contains(stdout.String(), forbidden) || strings.Contains(stderr.String(), forbidden) {
			t.Fatalf("check output contains %q; stdout = %q; stderr = %q", forbidden, stdout.String(), stderr.String())
		}
	}
}

func TestRunOffersAcceptsOnlySupportedProviderSelections(t *testing.T) {
	tests := []struct {
		selection string
		adapters  []provider.Adapter
	}{
		{selection: "all", adapters: []provider.Adapter{offersSuccessAdapter(provider.ProviderRunPod, nil, nil), offersSuccessAdapter(provider.ProviderVastAI, nil, nil), offersSuccessAdapter(provider.ProviderLambda, nil, nil), offersSuccessAdapter(provider.ProviderDigitalOcean, nil, nil)}},
		{selection: "runpod", adapters: []provider.Adapter{offersSuccessAdapter(provider.ProviderRunPod, nil, nil)}},
		{selection: "vastai", adapters: []provider.Adapter{offersSuccessAdapter(provider.ProviderVastAI, nil, nil)}},
		{selection: "lambda", adapters: []provider.Adapter{offersSuccessAdapter(provider.ProviderLambda, nil, nil)}},
		{selection: "digitalocean", adapters: []provider.Adapter{offersSuccessAdapter(provider.ProviderDigitalOcean, nil, nil)}},
	}
	for _, test := range tests {
		t.Run(test.selection, func(t *testing.T) {
			deps := configuredOffersDependencies(test.adapters)
			var gotSelection string
			baseFactory := deps.newAdapters
			deps.newAdapters = func(selection string, credentials map[provider.Provider]string, client *http.Client, now func() time.Time) ([]provider.Adapter, error) {
				gotSelection = selection
				return baseFactory(selection, credentials, client, now)
			}
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			code := run([]string{"offers", "-provider", test.selection, "-output", "check"}, &stdout, &stderr, deps)
			if code != 0 || gotSelection != test.selection {
				t.Fatalf("code = %d selection = %q; stdout = %q; stderr = %q", code, gotSelection, stdout.String(), stderr.String())
			}
		})
	}
}

func TestRunOffersRejectsInvalidArguments(t *testing.T) {
	tests := [][]string{
		{"offers", "-provider", "aws"},
		{"offers", "-provider="},
		{"offers", "-output", "yaml"},
		{"offers", "-timeout", "999ms"},
		{"offers", "-timeout", "2m0.000000001s"},
		{"offers", "-timeout", "not-a-duration"},
		{"offers", "unexpected"},
		{"offers", "-unknown"},
	}
	for _, args := range tests {
		t.Run(strings.Join(args[1:], " "), func(t *testing.T) {
			var factoryCalls atomic.Int32
			deps := configuredOffersDependencies(nil)
			deps.newAdapters = func(string, map[provider.Provider]string, *http.Client, func() time.Time) ([]provider.Adapter, error) {
				factoryCalls.Add(1)
				return nil, nil
			}
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			code := run(args, &stdout, &stderr, deps)
			if code != 2 || stdout.Len() != 0 || stderr.Len() == 0 || factoryCalls.Load() != 0 {
				t.Fatalf("args = %q code = %d factory calls = %d; stdout = %q; stderr = %q", args, code, factoryCalls.Load(), stdout.String(), stderr.String())
			}
		})
	}
}

func TestRunOffersInvalidArgumentsDoNotEchoInputs(t *testing.T) {
	const secret = offersTestCredential
	tests := []struct {
		name       string
		args       []string
		wantStderr string
	}{
		{
			name:       "provider value",
			args:       []string{"offers", "-provider", secret},
			wantStderr: "offers: provider must be one of all, runpod, vastai, lambda, or digitalocean\n",
		},
		{
			name:       "output value",
			args:       []string{"offers", "-output", secret},
			wantStderr: "offers: output must be one of text, json, or check\n",
		},
		{
			name:       "positional argument",
			args:       []string{"offers", secret},
			wantStderr: "offers: positional arguments are not supported\n",
		},
		{
			name:       "invalid duration",
			args:       []string{"offers", "-timeout", secret},
			wantStderr: "offers: invalid arguments; use -provider all|runpod|vastai|lambda|digitalocean, -output text|json|check, and -timeout 1s..2m0s\n",
		},
		{
			name:       "unknown flag name",
			args:       []string{"offers", "-" + secret},
			wantStderr: "offers: invalid arguments; use -provider all|runpod|vastai|lambda|digitalocean, -output text|json|check, and -timeout 1s..2m0s\n",
		},
		{
			name:       "unknown flag value",
			args:       []string{"offers", "-unknown=" + secret},
			wantStderr: "offers: invalid arguments; use -provider all|runpod|vastai|lambda|digitalocean, -output text|json|check, and -timeout 1s..2m0s\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var factoryCalls atomic.Int32
			deps := configuredOffersDependencies(nil)
			deps.newAdapters = func(string, map[provider.Provider]string, *http.Client, func() time.Time) ([]provider.Adapter, error) {
				factoryCalls.Add(1)
				return nil, nil
			}
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			code := run(test.args, &stdout, &stderr, deps)
			if code != 2 || factoryCalls.Load() != 0 {
				t.Fatalf("code = %d factory calls = %d, want 2 and 0", code, factoryCalls.Load())
			}
			if strings.Contains(stdout.String(), secret) || strings.Contains(stderr.String(), secret) {
				t.Fatalf("input leaked; stdout = %q; stderr = %q", stdout.String(), stderr.String())
			}
			if stdout.Len() != 0 || stderr.String() != test.wantStderr {
				t.Fatalf("stdout = %q; stderr = %q, want empty stdout and %q", stdout.String(), stderr.String(), test.wantStderr)
			}
		})
	}
}

func TestRunOffersHelpDoesNotEchoTrailingInput(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"offers", "-h", offersTestCredential}, &stdout, &stderr, configuredOffersDependencies(nil))
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	if strings.Contains(stdout.String(), offersTestCredential) || strings.Contains(stderr.String(), offersTestCredential) {
		t.Fatalf("input leaked; stdout = %q; stderr = %q", stdout.String(), stderr.String())
	}
	const wantStderr = "offers options: -provider all|runpod|vastai|lambda|digitalocean, -output text|json|check, -timeout 1s..2m0s\n"
	if stdout.Len() != 0 || stderr.String() != wantStderr {
		t.Fatalf("stdout = %q; stderr = %q, want empty stdout and %q", stdout.String(), stderr.String(), wantStderr)
	}
}

func TestRunOffersExitStatusUsesConfiguredResults(t *testing.T) {
	partial := mustOffersRejected(t, provider.ProviderVastAI, "partial", "Synthetic partial")
	tests := []struct {
		name     string
		adapters []provider.Adapter
		wantCode int
	}{
		{name: "success with failure", adapters: []provider.Adapter{offersSuccessAdapter(provider.ProviderRunPod, nil, nil), offersFailedAdapter(provider.ProviderLambda, errors.New("synthetic body"))}, wantCode: 0},
		{name: "partial with failure", adapters: []provider.Adapter{offersPartialAdapter(provider.ProviderVastAI, []provider.Disposition{partial}, provider.FailureRequest), offersFailedAdapter(provider.ProviderLambda, errors.New("synthetic body"))}, wantCode: 0},
		{name: "failure with skipped", adapters: []provider.Adapter{offersFailedAdapter(provider.ProviderLambda, errors.New("synthetic body")), offersSkippedAdapter(provider.ProviderDigitalOcean)}, wantCode: 1},
		{name: "all failed", adapters: []provider.Adapter{offersFailedAdapter(provider.ProviderRunPod, errors.New("synthetic body")), offersFailedAdapter(provider.ProviderLambda, errors.New("synthetic body"))}, wantCode: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			code := run([]string{"offers", "-output", "check"}, &stdout, &stderr, configuredOffersDependencies(test.adapters))
			if code != test.wantCode {
				t.Fatalf("code = %d, want %d; stdout = %q; stderr = %q", code, test.wantCode, stdout.String(), stderr.String())
			}
		})
	}
}

func TestRunOffersWriterFailuresAreSafe(t *testing.T) {
	for _, output := range []string{"text", "json", "check"} {
		t.Run(output, func(t *testing.T) {
			var stderr bytes.Buffer
			code := run([]string{"offers", "-output", output}, failingWriter{}, &stderr, configuredOffersDependencies([]provider.Adapter{offersSuccessAdapter(provider.ProviderRunPod, nil, nil)}))
			if code != 1 {
				t.Fatalf("code = %d, want 1", code)
			}
			const wantStderr = "offers: write output\n"
			if stderr.String() != wantStderr {
				t.Fatalf("stderr = %q, want %q", stderr.String(), wantStderr)
			}
		})
	}
}

func TestWriteOffersOutputRejectsShortWrite(t *testing.T) {
	err := writeOffersOutput(offersShortWriter{}, "check", offersTestNow, []provider.ProviderResult{{
		Provider: provider.ProviderRunPod,
		Status:   provider.ResultSuccess,
		Coverage: provider.Coverage{Complete: true},
	}})
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("error = %v, want io.ErrShortWrite", err)
	}
}

func TestRunOffersShortWriterFailureIsSafe(t *testing.T) {
	var stderr bytes.Buffer
	code := run([]string{"offers", "-output", "check"}, offersShortWriter{}, &stderr, configuredOffersDependencies([]provider.Adapter{offersSuccessAdapter(provider.ProviderRunPod, nil, nil)}))
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	const wantStderr = "offers: write output\n"
	if stderr.String() != wantStderr {
		t.Fatalf("stderr = %q, want %q", stderr.String(), wantStderr)
	}
}

func TestRunOffersInitializationFailuresAreSafe(t *testing.T) {
	deps := configuredOffersDependencies(nil)
	deps.newAdapters = func(string, map[provider.Provider]string, *http.Client, func() time.Time) ([]provider.Adapter, error) {
		return nil, errors.New("synthetic-private-initialization-body")
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"offers"}, &stdout, &stderr, deps)
	if code != 1 || stdout.Len() != 0 || stderr.String() != "offers: initialize providers\n" {
		t.Fatalf("code = %d; stdout = %q; stderr = %q", code, stdout.String(), stderr.String())
	}
}

func TestRunOffersTimeoutBoundariesReachTheSingleOperationContext(t *testing.T) {
	for _, value := range []string{"1s", "2m"} {
		t.Run(value, func(t *testing.T) {
			want, err := time.ParseDuration(value)
			if err != nil {
				t.Fatal(err)
			}
			var deadline time.Time
			adapter := &offersFakeAdapter{
				providerName: provider.ProviderRunPod,
				coverage:     offersBaselineCoverage(provider.ProviderRunPod),
				discover: func(ctx context.Context) (provider.Snapshot, error) {
					var ok bool
					deadline, ok = ctx.Deadline()
					if !ok {
						return provider.Snapshot{}, errors.New("operation context has no deadline")
					}
					return provider.Snapshot{Coverage: provider.Coverage{Market: "synthetic-runpod", Complete: true}}, nil
				},
			}
			start := time.Now()
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			code := run([]string{"offers", "-provider", "runpod", "-output", "check", "-timeout", value}, &stdout, &stderr, configuredOffersDependencies([]provider.Adapter{adapter}))
			if code != 0 {
				t.Fatalf("code = %d; stdout = %q; stderr = %q", code, stdout.String(), stderr.String())
			}
			propagated := deadline.Sub(start)
			if propagated < want || propagated > want+time.Second {
				t.Fatalf("operation deadline from start = %s, want [%s,%s]", propagated, want, want+time.Second)
			}
		})
	}
}

func TestRunOffersStartsAdaptersConcurrentlyAndRetainsPartialResults(t *testing.T) {
	type start struct {
		provider provider.Provider
		ctx      context.Context
	}
	started := make(chan start, 2)
	release := make(chan struct{})
	partialDisposition := mustOffersRejected(t, provider.ProviderVastAI, "partial-offer", "Synthetic partial")
	adapter := func(providerName provider.Provider) provider.Adapter {
		return &offersFakeAdapter{
			providerName: providerName,
			coverage:     offersBaselineCoverage(providerName),
			discover: func(ctx context.Context) (provider.Snapshot, error) {
				started <- start{provider: providerName, ctx: ctx}
				<-release
				if providerName == provider.ProviderVastAI {
					return provider.Snapshot{Dispositions: []provider.Disposition{partialDisposition}, Coverage: offersBaselineCoverage(providerName)}, &provider.AdapterError{Provider: providerName, Code: provider.FailureRequest}
				}
				return provider.Snapshot{Coverage: provider.Coverage{Market: "synthetic-" + string(providerName), Complete: true}}, nil
			},
		}
	}
	deps := configuredOffersDependencies([]provider.Adapter{adapter(provider.ProviderVastAI), adapter(provider.ProviderRunPod)})
	type outcome struct {
		code   int
		stdout string
		stderr string
	}
	result := make(chan outcome, 1)
	go func() {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		code := run([]string{"offers", "-output", "json"}, &stdout, &stderr, deps)
		result <- outcome{code: code, stdout: stdout.String(), stderr: stderr.String()}
	}()

	starts := make([]start, 0, 2)
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for len(starts) < 2 {
		select {
		case item := <-started:
			starts = append(starts, item)
		case <-timer.C:
			close(release)
			t.Fatal("adapters did not start concurrently")
		}
	}
	if starts[0].ctx != starts[1].ctx {
		close(release)
		t.Fatal("adapters received different operation contexts")
	}
	close(release)
	got := <-result
	if got.code != 0 || got.stderr != "" {
		t.Fatalf("code = %d; stdout = %q; stderr = %q", got.code, got.stdout, got.stderr)
	}
	var output struct {
		Providers []provider.ProviderResult `json:"providers"`
	}
	if err := json.Unmarshal([]byte(got.stdout), &output); err != nil {
		t.Fatal(err)
	}
	if len(output.Providers) != 2 || output.Providers[0].Status != provider.ResultSuccess || output.Providers[1].Status != provider.ResultPartial || len(output.Providers[1].Dispositions) != 1 {
		t.Fatalf("providers = %#v", output.Providers)
	}
}

func TestRunOffersWholeDeadlineStopsLaterRequests(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		<-request.Context().Done()
	}))
	t.Cleanup(server.Close)
	client := server.Client()
	adapterDone := make(chan struct{})
	adapter := &offersFakeAdapter{
		providerName: provider.ProviderRunPod,
		coverage:     offersBaselineCoverage(provider.ProviderRunPod),
		discover: func(ctx context.Context) (provider.Snapshot, error) {
			defer close(adapterDone)
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/page/1", nil)
			if err != nil {
				return provider.Snapshot{}, err
			}
			response, err := client.Do(request)
			if err != nil {
				return provider.Snapshot{}, err
			}
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()

			request, err = http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/page/2", nil)
			if err != nil {
				return provider.Snapshot{}, err
			}
			response, err = client.Do(request)
			if err == nil {
				_ = response.Body.Close()
			}
			return provider.Snapshot{}, err
		},
	}
	deps := configuredOffersDependencies([]provider.Adapter{adapter})
	deps.httpClient = client
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"offers", "-provider", "runpod", "-output", "check", "-timeout", "1s"}, &stdout, &stderr, deps)
	if code != 1 || stderr.Len() != 0 {
		t.Fatalf("code = %d; stdout = %q; stderr = %q", code, stdout.String(), stderr.String())
	}
	const want = "provider=runpod status=failed coverage=incomplete failure=timeout\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
	stopTimer := time.NewTimer(2 * time.Second)
	defer stopTimer.Stop()
	select {
	case <-adapterDone:
	case <-stopTimer.C:
		t.Fatal("adapter did not stop after the operation deadline")
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want only the first page", requests.Load())
	}
}

func configuredOffersDependencies(adapters []provider.Adapter) dependencies {
	return dependencies{
		getenv:     func(string) string { return offersTestCredential },
		httpClient: &http.Client{},
		now:        func() time.Time { return offersTestNow },
		newAdapters: func(string, map[provider.Provider]string, *http.Client, func() time.Time) ([]provider.Adapter, error) {
			return adapters, nil
		},
	}
}

func offersSuccessAdapter(providerName provider.Provider, dispositions []provider.Disposition, warnings []provider.Reason) provider.Adapter {
	return &offersFakeAdapter{
		providerName: providerName,
		coverage:     offersBaselineCoverage(providerName),
		discover: func(context.Context) (provider.Snapshot, error) {
			return provider.Snapshot{
				Dispositions: dispositions,
				Warnings:     warnings,
				Coverage:     provider.Coverage{Market: "synthetic-" + string(providerName), Complete: true},
			}, nil
		},
	}
}

func offersPartialAdapter(providerName provider.Provider, dispositions []provider.Disposition, code provider.FailureCode) provider.Adapter {
	return &offersFakeAdapter{
		providerName: providerName,
		coverage:     offersBaselineCoverage(providerName),
		discover: func(context.Context) (provider.Snapshot, error) {
			return provider.Snapshot{
				Dispositions: dispositions,
				Coverage:     offersBaselineCoverage(providerName),
			}, &provider.AdapterError{Provider: providerName, Code: code}
		},
	}
}

func offersTruncatedAdapter(providerName provider.Provider) provider.Adapter {
	return &offersFakeAdapter{
		providerName: providerName,
		coverage:     offersBaselineCoverage(providerName),
		discover: func(context.Context) (provider.Snapshot, error) {
			return provider.Snapshot{Coverage: provider.Coverage{
				Market:    "synthetic-" + string(providerName),
				Truncated: true,
				Notes:     []provider.Reason{{Code: provider.ReasonResultsTruncated, Field: "catalog", Message: "Synthetic catalog truncated"}},
			}}, nil
		},
	}
}

func offersFailedAdapter(providerName provider.Provider, err error) provider.Adapter {
	return &offersFakeAdapter{
		providerName: providerName,
		coverage:     offersBaselineCoverage(providerName),
		discover: func(context.Context) (provider.Snapshot, error) {
			return provider.Snapshot{}, err
		},
	}
}

func offersSkippedAdapter(providerName provider.Provider) provider.Adapter {
	return offersFailedAdapter(providerName, &provider.AdapterError{Provider: providerName, Code: provider.FailureNotConfigured})
}

func offersBaselineCoverage(providerName provider.Provider) provider.Coverage {
	return provider.Coverage{Market: "synthetic-" + string(providerName)}
}

func mustOffersAccepted(t *testing.T, providerName provider.Provider, id, model, price string) provider.Disposition {
	t.Helper()
	disposition, err := provider.Accepted(provider.Offer{
		Provider:        providerName,
		ProviderOfferID: id,
		GPUModel:        model,
		GPUCount:        1,
		Region:          "<region&>",
		CountryCode:     "US",
		Availability:    provider.AvailabilityAvailable,
		Billing: provider.Billing{
			Mode:                    "hourly",
			QuotedUSDPerHour:        price,
			RateScope:               provider.RateScopeTotal,
			BillingIncrementSeconds: 60,
		},
		ObservedAt: offersTestNow,
		SourceURL:  "https://synthetic.example.test/offers",
	})
	if err != nil {
		t.Fatal(err)
	}
	return disposition
}

func mustOffersRejected(t *testing.T, providerName provider.Provider, id, message string) provider.Disposition {
	t.Helper()
	disposition, err := provider.Rejected(providerName, id, provider.Reason{Code: provider.ReasonInvalidValue, Field: "gpu_model", Message: message})
	if err != nil {
		t.Fatal(err)
	}
	return disposition
}

func offersDispositionIDs(dispositions []provider.Disposition) []string {
	ids := make([]string, len(dispositions))
	for index, disposition := range dispositions {
		ids[index] = disposition.SourceID
	}
	return ids
}

func mapValues(values map[string]string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	return result
}
