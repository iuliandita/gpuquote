package workload

import (
	"fmt"
	"math"
	"strings"
)

type Mode string

const (
	ModeInteractive Mode = "interactive"
	ModeBatch       Mode = "batch"
)

type Profile struct {
	Preset                string  `json:"preset"`
	PresetVersion         int     `json:"preset_version"`
	Mode                  Mode    `json:"mode"`
	ConcurrentRequests    int     `json:"concurrent_requests,omitempty"`
	RequestsPerSecond     float64 `json:"requests_per_second,omitempty"`
	InputTokens           int64   `json:"input_tokens,omitempty"`
	OutputTokens          int64   `json:"output_tokens,omitempty"`
	ActiveHoursPerDay     float64 `json:"active_hours_per_day,omitempty"`
	MaxTTFTMilliseconds   int64   `json:"max_ttft_milliseconds,omitempty"`
	MinDecodeTokensPerSec float64 `json:"min_decode_tokens_per_second,omitempty"`
	TotalInputTokens      int64   `json:"total_input_tokens,omitempty"`
	TotalOutputTokens     int64   `json:"total_output_tokens,omitempty"`
	DeadlineSeconds       int64   `json:"deadline_seconds,omitempty"`
	ShutdownOnCompletion  bool    `json:"shutdown_on_completion"`
}

func (p Profile) Validate() error {
	if strings.TrimSpace(p.Preset) == "" {
		return fmt.Errorf("preset must not be empty")
	}
	if p.PresetVersion < 1 {
		return fmt.Errorf("preset_version must be at least 1")
	}

	numericFields := []struct {
		name  string
		value float64
	}{
		{name: "concurrent_requests", value: float64(p.ConcurrentRequests)},
		{name: "requests_per_second", value: p.RequestsPerSecond},
		{name: "input_tokens", value: float64(p.InputTokens)},
		{name: "output_tokens", value: float64(p.OutputTokens)},
		{name: "active_hours_per_day", value: p.ActiveHoursPerDay},
		{name: "max_ttft_milliseconds", value: float64(p.MaxTTFTMilliseconds)},
		{name: "min_decode_tokens_per_second", value: p.MinDecodeTokensPerSec},
		{name: "total_input_tokens", value: float64(p.TotalInputTokens)},
		{name: "total_output_tokens", value: float64(p.TotalOutputTokens)},
		{name: "deadline_seconds", value: float64(p.DeadlineSeconds)},
	}
	for _, field := range numericFields {
		if err := validateNonNegative(field.name, field.value); err != nil {
			return err
		}
	}

	switch p.Mode {
	case ModeInteractive:
		if p.InputTokens == 0 {
			return fmt.Errorf("interactive input_tokens must be greater than zero")
		}
		if p.OutputTokens == 0 {
			return fmt.Errorf("interactive output_tokens must be greater than zero")
		}
		if p.ConcurrentRequests < 1 && p.RequestsPerSecond == 0 {
			return fmt.Errorf("interactive profile requires concurrent_requests at least 1 or requests_per_second greater than zero")
		}
		if p.ActiveHoursPerDay == 0 || p.ActiveHoursPerDay > 24 {
			return fmt.Errorf("interactive active_hours_per_day must be greater than zero and at most 24")
		}
		if p.TotalInputTokens != 0 || p.TotalOutputTokens != 0 || p.DeadlineSeconds != 0 || p.ShutdownOnCompletion {
			return fmt.Errorf("interactive profile must not set batch fields")
		}
		return nil

	case ModeBatch:
		if p.TotalInputTokens == 0 {
			return fmt.Errorf("batch total_input_tokens must be greater than zero")
		}
		if p.TotalOutputTokens == 0 {
			return fmt.Errorf("batch total_output_tokens must be greater than zero")
		}
		if p.DeadlineSeconds == 0 {
			return fmt.Errorf("batch deadline_seconds must be greater than zero")
		}
		if p.ConcurrentRequests != 0 {
			return fmt.Errorf("batch concurrent_requests must be zero")
		}
		if p.RequestsPerSecond != 0 {
			return fmt.Errorf("batch requests_per_second must be zero")
		}
		if p.InputTokens != 0 {
			return fmt.Errorf("batch input_tokens must be zero")
		}
		if p.OutputTokens != 0 {
			return fmt.Errorf("batch output_tokens must be zero")
		}
		if p.ActiveHoursPerDay != 0 {
			return fmt.Errorf("batch active_hours_per_day must be zero")
		}
		if p.MaxTTFTMilliseconds != 0 {
			return fmt.Errorf("batch max_ttft_milliseconds must be zero")
		}
		if p.MinDecodeTokensPerSec != 0 {
			return fmt.Errorf("batch min_decode_tokens_per_second must be zero")
		}
		return nil

	default:
		return fmt.Errorf("mode must be %q or %q, got %q", ModeInteractive, ModeBatch, p.Mode)
	}
}

func validateNonNegative(name string, value float64) error {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return fmt.Errorf("%s must be finite", name)
	}
	if value < 0 {
		return fmt.Errorf("%s must be nonnegative", name)
	}
	return nil
}
