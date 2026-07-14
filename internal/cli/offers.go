package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/iuliandita/gpuquote/internal/provider"
)

type dependencies struct {
	getenv      func(string) string
	httpClient  *http.Client
	now         func() time.Time
	newAdapters func(selection string, credentials map[provider.Provider]string, client *http.Client, now func() time.Time) ([]provider.Adapter, error)
}

type offersOutput struct {
	SchemaVersion int                       `json:"schema_version"`
	CollectedAt   time.Time                 `json:"collected_at"`
	Providers     []provider.ProviderResult `json:"providers"`
}

type providerCredential struct {
	provider provider.Provider
	envName  string
}

var providerCredentials = []providerCredential{
	{provider: provider.ProviderRunPod, envName: "RUNPOD_API_KEY"},
	{provider: provider.ProviderVastAI, envName: "VAST_API_KEY"},
	{provider: provider.ProviderLambda, envName: "LAMBDA_API_KEY"},
	{provider: provider.ProviderDigitalOcean, envName: "DIGITALOCEAN_TOKEN"},
}

const (
	offersOptions          = "offers options: -provider all|runpod|vastai|lambda|digitalocean, -output text|json|check, -timeout 1s..2m0s\n"
	offersInvalidArguments = "offers: invalid arguments; use -provider all|runpod|vastai|lambda|digitalocean, -output text|json|check, and -timeout 1s..2m0s\n"
)

func runOffers(args []string, stdout, stderr io.Writer, deps dependencies) int {
	flags := flag.NewFlagSet("offers", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	selection := flags.String("provider", "all", "provider: all, runpod, vastai, lambda, or digitalocean")
	output := flags.String("output", "text", "output format: text, json, or check")
	timeout := flags.Duration("timeout", 20*time.Second, "whole-operation timeout")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, _ = io.WriteString(stderr, offersOptions)
			return 0
		}
		_, _ = io.WriteString(stderr, offersInvalidArguments)
		return 2
	}
	if !validProviderSelection(*selection) {
		_, _ = io.WriteString(stderr, "offers: provider must be one of all, runpod, vastai, lambda, or digitalocean\n")
		return 2
	}
	if !validOffersOutput(*output) {
		_, _ = io.WriteString(stderr, "offers: output must be one of text, json, or check\n")
		return 2
	}
	if *timeout < time.Second || *timeout > 2*time.Minute {
		_, _ = io.WriteString(stderr, "offers: timeout must be between 1s and 2m0s\n")
		return 2
	}
	if flags.NArg() != 0 {
		_, _ = io.WriteString(stderr, "offers: positional arguments are not supported\n")
		return 2
	}

	collectedAt := deps.now().UTC()
	stableNow := func() time.Time { return collectedAt }
	credentials := loadProviderCredentials(deps.getenv)
	adapters, err := deps.newAdapters(*selection, credentials, deps.httpClient, stableNow)
	if err != nil {
		_, _ = io.WriteString(stderr, "offers: initialize providers\n")
		return 1
	}
	discoverer, err := provider.NewDiscoverer(adapters...)
	if err != nil {
		_, _ = io.WriteString(stderr, "offers: initialize providers\n")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	results := discoverer.Discover(ctx)
	if allProvidersSkipped(results) {
		_, _ = io.WriteString(stderr, "offers: no provider credentials configured; set RUNPOD_API_KEY, VAST_API_KEY, LAMBDA_API_KEY, or DIGITALOCEAN_TOKEN\n")
		return 2
	}
	if err := writeOffersOutput(stdout, *output, collectedAt, results); err != nil {
		_, _ = io.WriteString(stderr, "offers: write output\n")
		return 1
	}
	if anyProviderUsable(results) {
		return 0
	}
	return 1
}

func validProviderSelection(selection string) bool {
	switch selection {
	case "all", "runpod", "vastai", "lambda", "digitalocean":
		return true
	default:
		return false
	}
}

func validOffersOutput(output string) bool {
	switch output {
	case "text", "json", "check":
		return true
	default:
		return false
	}
}

func loadProviderCredentials(getenv func(string) string) map[provider.Provider]string {
	credentials := make(map[provider.Provider]string, len(providerCredentials))
	for _, credential := range providerCredentials {
		credentials[credential.provider] = getenv(credential.envName)
	}
	return credentials
}

func newProviderAdapters(selection string, credentials map[provider.Provider]string, client *http.Client, now func() time.Time) ([]provider.Adapter, error) {
	constructors := map[provider.Provider]func(string, *http.Client, func() time.Time) provider.Adapter{
		provider.ProviderRunPod:       provider.NewRunPod,
		provider.ProviderVastAI:       provider.NewVastAI,
		provider.ProviderLambda:       provider.NewLambda,
		provider.ProviderDigitalOcean: provider.NewDigitalOcean,
	}
	selected := []provider.Provider{
		provider.ProviderRunPod,
		provider.ProviderVastAI,
		provider.ProviderLambda,
		provider.ProviderDigitalOcean,
	}
	if selection != "all" {
		selected = nil
		for providerName := range constructors {
			if string(providerName) == selection {
				selected = []provider.Provider{providerName}
				break
			}
		}
	}
	if len(selected) == 0 {
		return nil, errors.New("provider selection is invalid")
	}
	adapters := make([]provider.Adapter, 0, len(selected))
	for _, providerName := range selected {
		adapters = append(adapters, constructors[providerName](credentials[providerName], client, now))
	}
	return adapters, nil
}

func allProvidersSkipped(results []provider.ProviderResult) bool {
	for _, result := range results {
		if result.Status != provider.ResultSkipped {
			return false
		}
	}
	return true
}

func anyProviderUsable(results []provider.ProviderResult) bool {
	for _, result := range results {
		if result.Status == provider.ResultSuccess || result.Status == provider.ResultPartial {
			return true
		}
	}
	return false
}

func writeOffersOutput(writer io.Writer, output string, collectedAt time.Time, results []provider.ProviderResult) error {
	var encoded bytes.Buffer
	switch output {
	case "json":
		encoder := json.NewEncoder(&encoded)
		encoder.SetEscapeHTML(true)
		if err := encoder.Encode(offersOutput{SchemaVersion: 1, CollectedAt: collectedAt, Providers: results}); err != nil {
			return err
		}
	case "text":
		writeOffersText(&encoded, results)
	case "check":
		writeOffersCheck(&encoded, results)
	default:
		return errors.New("unsupported offers output")
	}
	value := encoded.Bytes()
	written, err := writer.Write(value)
	if err != nil {
		return err
	}
	if written != len(value) {
		return io.ErrShortWrite
	}
	return nil
}

func writeOffersText(output *bytes.Buffer, results []provider.ProviderResult) {
	for _, result := range results {
		fmt.Fprintf(output, "provider=%s status=%s coverage=%s market=%q dispositions=%d warnings=%d", result.Provider, result.Status, coverageState(result.Coverage), result.Coverage.Market, len(result.Dispositions), len(result.Warnings))
		if result.Failure != nil {
			fmt.Fprintf(output, " failure=%q", result.Failure.Code)
		}
		output.WriteByte('\n')
		for _, disposition := range result.Dispositions {
			switch disposition.Status {
			case provider.DispositionAccepted:
				offer := disposition.Offer
				fmt.Fprintf(output, "accepted source_id=%q gpu_model=%q gpu_count=%d region=%q country=%q price_usd_per_hour=%q rate_scope=%q\n", disposition.SourceID, offer.GPUModel, offer.GPUCount, offer.Region, offer.CountryCode, offer.Billing.QuotedUSDPerHour, offer.Billing.RateScope)
			case provider.DispositionRejected:
				for _, reason := range disposition.Reasons {
					fmt.Fprintf(output, "rejected source_id=%q reason=%q field=%q message=%q\n", disposition.SourceID, reason.Code, reason.Field, reason.Message)
				}
			}
		}
	}
}

func writeOffersCheck(output *bytes.Buffer, results []provider.ProviderResult) {
	for _, result := range results {
		fmt.Fprintf(output, "provider=%s status=%s coverage=%s", result.Provider, result.Status, coverageState(result.Coverage))
		if result.Failure != nil {
			fmt.Fprintf(output, " failure=%s", result.Failure.Code)
		}
		output.WriteByte('\n')
	}
}

func coverageState(coverage provider.Coverage) string {
	switch {
	case coverage.Truncated:
		return "truncated"
	case coverage.Complete:
		return "complete"
	default:
		return "incomplete"
	}
}
