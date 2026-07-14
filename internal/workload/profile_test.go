package workload

import (
	"encoding/json"
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

func modifiedInteractiveProfile(modify func(*Profile)) Profile {
	profile := validInteractiveProfile()
	modify(&profile)
	return profile
}

func modifiedBatchProfile(modify func(*Profile)) Profile {
	profile := validBatchProfile()
	modify(&profile)
	return profile
}

func TestProfileMarshalJSONMatchesContract(t *testing.T) {
	batchProfile := validBatchProfile()
	batchProfile.ShutdownOnCompletion = false

	tests := []struct {
		name    string
		profile Profile
		want    string
	}{
		{
			name:    "interactive",
			profile: validInteractiveProfile(),
			want:    `{"preset":"personal-chat","preset_version":1,"mode":"interactive","concurrent_requests":1,"input_tokens":2048,"output_tokens":512,"active_hours_per_day":4,"max_ttft_milliseconds":5000,"min_decode_tokens_per_second":10,"shutdown_on_completion":false}`,
		},
		{
			name:    "batch",
			profile: batchProfile,
			want:    `{"preset":"batch-job","preset_version":1,"mode":"batch","total_input_tokens":1000000,"total_output_tokens":250000,"deadline_seconds":14400,"shutdown_on_completion":false}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := json.Marshal(test.profile)
			if err != nil {
				t.Fatalf("json.Marshal() error = %v", err)
			}
			if string(got) != test.want {
				t.Errorf("json.Marshal() = %s, want %s", got, test.want)
			}
		})
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
		wantErr string
	}{
		{
			name:    "empty preset",
			profile: modifiedInteractiveProfile(func(profile *Profile) { profile.Preset = "" }),
			wantErr: "preset must not be empty",
		},
		{
			name:    "preset version below one",
			profile: modifiedInteractiveProfile(func(profile *Profile) { profile.PresetVersion = 0 }),
			wantErr: "preset_version must be at least 1",
		},
		{
			name:    "unknown mode",
			profile: modifiedInteractiveProfile(func(profile *Profile) { profile.Mode = Mode("unknown") }),
			wantErr: `mode must be "interactive" or "batch", got "unknown"`,
		},
		{
			name: "interactive without concurrency or request rate",
			profile: modifiedInteractiveProfile(func(profile *Profile) {
				profile.ConcurrentRequests = 0
				profile.RequestsPerSecond = 0
			}),
			wantErr: "interactive profile requires concurrent_requests at least 1 or requests_per_second greater than zero",
		},
		{
			name:    "active hours above daily limit",
			profile: modifiedInteractiveProfile(func(profile *Profile) { profile.ActiveHoursPerDay = 25 }),
			wantErr: "interactive active_hours_per_day must be greater than zero and at most 24",
		},
		{
			name:    "interactive with batch totals",
			profile: modifiedInteractiveProfile(func(profile *Profile) { profile.TotalInputTokens = 1 }),
			wantErr: "interactive profile must not set batch fields",
		},
		{
			name:    "interactive with shutdown on completion",
			profile: modifiedInteractiveProfile(func(profile *Profile) { profile.ShutdownOnCompletion = true }),
			wantErr: "interactive profile must not set batch fields",
		},
		{
			name:    "batch with interactive concurrency",
			profile: modifiedBatchProfile(func(profile *Profile) { profile.ConcurrentRequests = 1 }),
			wantErr: "batch concurrent_requests must be zero",
		},
		{
			name:    "batch with interactive request rate",
			profile: modifiedBatchProfile(func(profile *Profile) { profile.RequestsPerSecond = 1 }),
			wantErr: "batch requests_per_second must be zero",
		},
		{
			name:    "batch without deadline",
			profile: modifiedBatchProfile(func(profile *Profile) { profile.DeadlineSeconds = 0 }),
			wantErr: "batch deadline_seconds must be greater than zero",
		},
		{
			name:    "interactive with infinite request rate",
			profile: modifiedInteractiveProfile(func(profile *Profile) { profile.RequestsPerSecond = math.Inf(1) }),
			wantErr: "requests_per_second must be finite",
		},
		{
			name:    "interactive with NaN decode rate",
			profile: modifiedInteractiveProfile(func(profile *Profile) { profile.MinDecodeTokensPerSec = math.NaN() }),
			wantErr: "min_decode_tokens_per_second must be finite",
		},
		{
			name:    "interactive with negative maximum TTFT",
			profile: modifiedInteractiveProfile(func(profile *Profile) { profile.MaxTTFTMilliseconds = -1 }),
			wantErr: "max_ttft_milliseconds must be nonnegative",
		},
		{
			name:    "interactive without input tokens",
			profile: modifiedInteractiveProfile(func(profile *Profile) { profile.InputTokens = 0 }),
			wantErr: "interactive input_tokens must be greater than zero",
		},
		{
			name:    "interactive without output tokens",
			profile: modifiedInteractiveProfile(func(profile *Profile) { profile.OutputTokens = 0 }),
			wantErr: "interactive output_tokens must be greater than zero",
		},
		{
			name:    "interactive without active hours",
			profile: modifiedInteractiveProfile(func(profile *Profile) { profile.ActiveHoursPerDay = 0 }),
			wantErr: "interactive active_hours_per_day must be greater than zero and at most 24",
		},
		{
			name:    "batch without total input tokens",
			profile: modifiedBatchProfile(func(profile *Profile) { profile.TotalInputTokens = 0 }),
			wantErr: "batch total_input_tokens must be greater than zero",
		},
		{
			name:    "batch without total output tokens",
			profile: modifiedBatchProfile(func(profile *Profile) { profile.TotalOutputTokens = 0 }),
			wantErr: "batch total_output_tokens must be greater than zero",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.profile.Validate()
			if err == nil {
				t.Fatal("Profile.Validate() error = nil, want non-nil")
			}
			if err.Error() != test.wantErr {
				t.Errorf("Profile.Validate() error = %q, want %q", err, test.wantErr)
			}
		})
	}
}
