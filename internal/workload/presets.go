package workload

import (
	"fmt"
	"sort"
	"strings"
)

const PresetVersion = 1

var presets = map[string]Profile{
	"batch-job": {
		Preset:               "batch-job",
		PresetVersion:        PresetVersion,
		Mode:                 ModeBatch,
		TotalInputTokens:     1_000_000,
		TotalOutputTokens:    250_000,
		DeadlineSeconds:      14_400,
		ShutdownOnCompletion: true,
	},
	"coding-agent": {
		Preset:             "coding-agent",
		PresetVersion:      PresetVersion,
		Mode:               ModeInteractive,
		ConcurrentRequests: 2,
		InputTokens:        8192,
		OutputTokens:       2048,
		ActiveHoursPerDay:  8,
	},
	"interactive-service": {
		Preset:                "interactive-service",
		PresetVersion:         PresetVersion,
		Mode:                  ModeInteractive,
		RequestsPerSecond:     1,
		InputTokens:           2048,
		OutputTokens:          512,
		ActiveHoursPerDay:     24,
		MaxTTFTMilliseconds:   2000,
		MinDecodeTokensPerSec: 20,
	},
	"personal-chat": {
		Preset:             "personal-chat",
		PresetVersion:      PresetVersion,
		Mode:               ModeInteractive,
		ConcurrentRequests: 1,
		InputTokens:        2048,
		OutputTokens:       512,
		ActiveHoursPerDay:  4,
	},
}

func Resolve(name string) (Profile, error) {
	profile, ok := presets[strings.TrimSpace(name)]
	if !ok {
		return Profile{}, fmt.Errorf("unknown preset %q", name)
	}
	return profile, nil
}

func List() []Profile {
	profiles := make([]Profile, 0, len(presets))
	for _, profile := range presets {
		profiles = append(profiles, profile)
	}
	sort.Slice(profiles, func(i, j int) bool {
		return profiles[i].Preset < profiles[j].Preset
	})
	return profiles
}
