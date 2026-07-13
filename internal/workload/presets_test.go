package workload

import (
	"reflect"
	"slices"
	"testing"
)

func TestListReturnsValidSortedPresets(t *testing.T) {
	profiles := List()
	names := make([]string, 0, len(profiles))
	for _, profile := range profiles {
		names = append(names, profile.Preset)
		if err := profile.Validate(); err != nil {
			t.Errorf("Profile.Validate() for %q error = %v, want nil", profile.Preset, err)
		}
	}

	want := []string{"batch-job", "coding-agent", "interactive-service", "personal-chat"}
	if !slices.Equal(names, want) {
		t.Errorf("List() preset names = %v, want %v", names, want)
	}
}

func TestResolveReturnsCopyAndRejectsUnknownPreset(t *testing.T) {
	profile, err := Resolve("coding-agent")
	if err != nil {
		t.Fatalf("Resolve() error = %v, want nil", err)
	}
	profile.ConcurrentRequests = 99

	profile, err = Resolve("coding-agent")
	if err != nil {
		t.Fatalf("Resolve() second call error = %v, want nil", err)
	}
	if profile.ConcurrentRequests != 2 {
		t.Errorf("Resolve() ConcurrentRequests = %d, want 2", profile.ConcurrentRequests)
	}

	if _, err := Resolve("missing"); err == nil {
		t.Fatal("Resolve() error = nil, want non-nil")
	}
}

func TestPresetsMatchApprovedValues(t *testing.T) {
	want := []Profile{
		{
			Preset:               "batch-job",
			PresetVersion:        1,
			Mode:                 ModeBatch,
			TotalInputTokens:     1_000_000,
			TotalOutputTokens:    250_000,
			DeadlineSeconds:      14_400,
			ShutdownOnCompletion: true,
		},
		{
			Preset:             "coding-agent",
			PresetVersion:      1,
			Mode:               ModeInteractive,
			ConcurrentRequests: 2,
			InputTokens:        8192,
			OutputTokens:       2048,
			ActiveHoursPerDay:  8,
		},
		{
			Preset:                "interactive-service",
			PresetVersion:         1,
			Mode:                  ModeInteractive,
			RequestsPerSecond:     1,
			InputTokens:           2048,
			OutputTokens:          512,
			ActiveHoursPerDay:     24,
			MaxTTFTMilliseconds:   2000,
			MinDecodeTokensPerSec: 20,
		},
		{
			Preset:             "personal-chat",
			PresetVersion:      1,
			Mode:               ModeInteractive,
			ConcurrentRequests: 1,
			InputTokens:        2048,
			OutputTokens:       512,
			ActiveHoursPerDay:  4,
		},
	}

	if got := List(); !reflect.DeepEqual(got, want) {
		t.Errorf("List() = %#v, want %#v", got, want)
	}
}
