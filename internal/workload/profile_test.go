package workload

import (
	"math"
	"testing"
)

func validInteractiveProfile() Profile {
	return Profile{
		Preset:                "personal-chat",
		PresetVersion:         1,
		Mode:                  ModeInteractive,
		ConcurrentRequests:    1,
		InputTokens:           2048,
		OutputTokens:          512,
		ActiveHoursPerDay:     4,
		MaxTTFTMilliseconds:   5000,
		MinDecodeTokensPerSec: 10,
	}
}

func validBatchProfile() Profile {
	return Profile{
		Preset:               "batch-job",
		PresetVersion:        1,
		Mode:                 ModeBatch,
		TotalInputTokens:     1_000_000,
		TotalOutputTokens:    250_000,
		DeadlineSeconds:      14_400,
		ShutdownOnCompletion: true,
	}
}

func TestProfileValidateAcceptsValidModes(t *testing.T) {
	profiles := []Profile{
		validInteractiveProfile(),
		validBatchProfile(),
	}

	for _, profile := range profiles {
		if err := profile.Validate(); err != nil {
			t.Errorf("Profile.Validate() error = %v, want nil", err)
		}
	}
}

func TestProfileValidateRejectsInvalidProfiles(t *testing.T) {
	tests := []struct {
		name    string
		profile Profile
	}{
		{
			name: "empty preset",
			profile: func() Profile {
				profile := validInteractiveProfile()
				profile.Preset = ""
				return profile
			}(),
		},
		{
			name: "unknown mode",
			profile: func() Profile {
				profile := validInteractiveProfile()
				profile.Mode = Mode("unknown")
				return profile
			}(),
		},
		{
			name: "interactive without concurrency or request rate",
			profile: func() Profile {
				profile := validInteractiveProfile()
				profile.ConcurrentRequests = 0
				profile.RequestsPerSecond = 0
				return profile
			}(),
		},
		{
			name: "active hours above daily limit",
			profile: func() Profile {
				profile := validInteractiveProfile()
				profile.ActiveHoursPerDay = 25
				return profile
			}(),
		},
		{
			name: "interactive with batch totals",
			profile: func() Profile {
				profile := validInteractiveProfile()
				profile.TotalInputTokens = 1
				return profile
			}(),
		},
		{
			name: "batch with interactive concurrency",
			profile: func() Profile {
				profile := validBatchProfile()
				profile.ConcurrentRequests = 1
				return profile
			}(),
		},
		{
			name: "batch without deadline",
			profile: func() Profile {
				profile := validBatchProfile()
				profile.DeadlineSeconds = 0
				return profile
			}(),
		},
		{
			name: "interactive with infinite request rate",
			profile: func() Profile {
				profile := validInteractiveProfile()
				profile.RequestsPerSecond = math.Inf(1)
				return profile
			}(),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.profile.Validate(); err == nil {
				t.Fatal("Profile.Validate() error = nil, want non-nil")
			}
		})
	}
}
