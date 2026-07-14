package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/iuliandita/gpuquote/internal/workload"
)

func runPresets(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("presets", flag.ContinueOnError)
	flags.SetOutput(stderr)
	output := flags.String("output", "text", "output format: text or json")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		_, _ = fmt.Fprintf(stderr, "presets: unexpected arguments: %q\n", flags.Args())
		return 2
	}
	if err := writeProfiles(stdout, workload.List(), *output); err != nil {
		_, _ = fmt.Fprintf(stderr, "presets: write output: %v\n", err)
		return 1
	}
	return 0
}

func runWorkload(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("workload", flag.ContinueOnError)
	flags.SetOutput(stderr)
	preset := flags.String("preset", "", "workload preset")
	output := flags.String("output", "text", "output format: text or json")
	concurrency := flags.Int("concurrency", -1, "concurrent requests")
	requestRate := flags.Float64("requests-per-second", -1, "request rate")
	inputTokens := flags.Int64("input-tokens", -1, "input tokens per request")
	outputTokens := flags.Int64("output-tokens", -1, "output tokens per request")
	activeHours := flags.Float64("active-hours-per-day", -1, "active hours per day")
	maxTTFT := flags.Int64("max-ttft-ms", -1, "maximum time to first token in milliseconds")
	minDecode := flags.Float64("min-decode-tps", -1, "minimum decode tokens per second")
	totalInput := flags.Int64("total-input-tokens", -1, "batch input token total")
	totalOutput := flags.Int64("total-output-tokens", -1, "batch output token total")
	deadline := flags.Int64("deadline-seconds", -1, "batch deadline in seconds")
	shutdown := flags.String("shutdown-on-completion", "", "true or false")

	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		_, _ = fmt.Fprintf(stderr, "workload: unexpected arguments: %q\n", flags.Args())
		return 2
	}
	if strings.TrimSpace(*preset) == "" {
		_, _ = fmt.Fprintln(stderr, "workload: -preset is required")
		return 2
	}

	profile, err := workload.Resolve(*preset)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "workload: %v\n", err)
		return 2
	}
	overrides, err := buildOverrides(*concurrency, *requestRate, *inputTokens, *outputTokens, *activeHours, *maxTTFT, *minDecode, *totalInput, *totalOutput, *deadline, *shutdown)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "workload: %v\n", err)
		return 2
	}
	profile, err = overrides.Apply(profile)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "workload: %v\n", err)
		return 2
	}
	if err := writeProfile(stdout, profile, *output); err != nil {
		_, _ = fmt.Fprintf(stderr, "workload: write output: %v\n", err)
		return 1
	}
	return 0
}

func buildOverrides(concurrency int, requestRate float64, inputTokens, outputTokens int64, activeHours float64, maxTTFT int64, minDecode float64, totalInput, totalOutput, deadline int64, shutdown string) (workload.Overrides, error) {
	var overrides workload.Overrides
	var err error
	if overrides.ConcurrentRequests, err = optionalInt("concurrency", concurrency); err != nil {
		return workload.Overrides{}, err
	}
	if overrides.RequestsPerSecond, err = optionalFloat("requests-per-second", requestRate); err != nil {
		return workload.Overrides{}, err
	}
	if overrides.InputTokens, err = optionalInt64("input-tokens", inputTokens); err != nil {
		return workload.Overrides{}, err
	}
	if overrides.OutputTokens, err = optionalInt64("output-tokens", outputTokens); err != nil {
		return workload.Overrides{}, err
	}
	if overrides.ActiveHoursPerDay, err = optionalFloat("active-hours-per-day", activeHours); err != nil {
		return workload.Overrides{}, err
	}
	if overrides.MaxTTFTMilliseconds, err = optionalInt64("max-ttft-ms", maxTTFT); err != nil {
		return workload.Overrides{}, err
	}
	if overrides.MinDecodeTokensPerSec, err = optionalFloat("min-decode-tps", minDecode); err != nil {
		return workload.Overrides{}, err
	}
	if overrides.TotalInputTokens, err = optionalInt64("total-input-tokens", totalInput); err != nil {
		return workload.Overrides{}, err
	}
	if overrides.TotalOutputTokens, err = optionalInt64("total-output-tokens", totalOutput); err != nil {
		return workload.Overrides{}, err
	}
	if overrides.DeadlineSeconds, err = optionalInt64("deadline-seconds", deadline); err != nil {
		return workload.Overrides{}, err
	}
	if shutdown != "" {
		value, parseErr := strconv.ParseBool(shutdown)
		if parseErr != nil {
			return workload.Overrides{}, fmt.Errorf("shutdown-on-completion: %w", parseErr)
		}
		overrides.ShutdownOnCompletion = &value
	}
	return overrides, nil
}

func optionalInt(name string, value int) (*int, error) {
	if value == -1 {
		return nil, nil
	}
	if value < 0 {
		return nil, fmt.Errorf("%s must be non-negative", name)
	}
	return &value, nil
}

func optionalInt64(name string, value int64) (*int64, error) {
	if value == -1 {
		return nil, nil
	}
	if value < 0 {
		return nil, fmt.Errorf("%s must be non-negative", name)
	}
	return &value, nil
}

func optionalFloat(name string, value float64) (*float64, error) {
	if value == -1 {
		return nil, nil
	}
	if value < 0 {
		return nil, fmt.Errorf("%s must be non-negative", name)
	}
	return &value, nil
}

func writeProfiles(writer io.Writer, profiles []workload.Profile, format string) error {
	switch format {
	case "json":
		return json.NewEncoder(writer).Encode(profiles)
	case "text":
		var output strings.Builder
		for _, profile := range profiles {
			fmt.Fprintf(&output, "%s v%d %s\n", profile.Preset, profile.PresetVersion, profile.Mode)
		}
		_, err := io.WriteString(writer, output.String())
		return err
	default:
		return fmt.Errorf("unsupported output format %q", format)
	}
}

func writeProfile(writer io.Writer, profile workload.Profile, format string) error {
	switch format {
	case "json":
		return json.NewEncoder(writer).Encode(profile)
	case "text":
		var output strings.Builder
		fmt.Fprintf(&output, "preset: %s\nversion: %d\nmode: %s\n", profile.Preset, profile.PresetVersion, profile.Mode)
		if profile.Mode == workload.ModeInteractive {
			fmt.Fprintf(&output, "concurrent_requests: %d\nrequests_per_second: %g\ninput_tokens: %d\noutput_tokens: %d\nactive_hours_per_day: %g\nmax_ttft_milliseconds: %d\nmin_decode_tokens_per_second: %g\n", profile.ConcurrentRequests, profile.RequestsPerSecond, profile.InputTokens, profile.OutputTokens, profile.ActiveHoursPerDay, profile.MaxTTFTMilliseconds, profile.MinDecodeTokensPerSec)
		} else {
			fmt.Fprintf(&output, "total_input_tokens: %d\ntotal_output_tokens: %d\ndeadline_seconds: %d\nshutdown_on_completion: %t\n", profile.TotalInputTokens, profile.TotalOutputTokens, profile.DeadlineSeconds, profile.ShutdownOnCompletion)
		}
		_, err := io.WriteString(writer, output.String())
		return err
	default:
		return fmt.Errorf("unsupported output format %q", format)
	}
}
