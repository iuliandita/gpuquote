package workload

import "fmt"

type Overrides struct {
	ConcurrentRequests    *int
	RequestsPerSecond     *float64
	InputTokens           *int64
	OutputTokens          *int64
	ActiveHoursPerDay     *float64
	MaxTTFTMilliseconds   *int64
	MinDecodeTokensPerSec *float64
	TotalInputTokens      *int64
	TotalOutputTokens     *int64
	DeadlineSeconds       *int64
	ShutdownOnCompletion  *bool
}

func (o Overrides) Apply(base Profile) (Profile, error) {
	if err := o.validateForMode(base.Mode); err != nil {
		return Profile{}, err
	}
	profile := base
	if o.ConcurrentRequests != nil {
		profile.ConcurrentRequests = *o.ConcurrentRequests
	}
	if o.RequestsPerSecond != nil {
		profile.RequestsPerSecond = *o.RequestsPerSecond
	}
	if o.InputTokens != nil {
		profile.InputTokens = *o.InputTokens
	}
	if o.OutputTokens != nil {
		profile.OutputTokens = *o.OutputTokens
	}
	if o.ActiveHoursPerDay != nil {
		profile.ActiveHoursPerDay = *o.ActiveHoursPerDay
	}
	if o.MaxTTFTMilliseconds != nil {
		profile.MaxTTFTMilliseconds = *o.MaxTTFTMilliseconds
	}
	if o.MinDecodeTokensPerSec != nil {
		profile.MinDecodeTokensPerSec = *o.MinDecodeTokensPerSec
	}
	if o.TotalInputTokens != nil {
		profile.TotalInputTokens = *o.TotalInputTokens
	}
	if o.TotalOutputTokens != nil {
		profile.TotalOutputTokens = *o.TotalOutputTokens
	}
	if o.DeadlineSeconds != nil {
		profile.DeadlineSeconds = *o.DeadlineSeconds
	}
	if o.ShutdownOnCompletion != nil {
		profile.ShutdownOnCompletion = *o.ShutdownOnCompletion
	}
	if err := profile.Validate(); err != nil {
		return Profile{}, err
	}
	return profile, nil
}

func (o Overrides) validateForMode(mode Mode) error {
	hasInteractive := o.ConcurrentRequests != nil || o.RequestsPerSecond != nil || o.InputTokens != nil || o.OutputTokens != nil || o.ActiveHoursPerDay != nil || o.MaxTTFTMilliseconds != nil || o.MinDecodeTokensPerSec != nil
	hasBatch := o.TotalInputTokens != nil || o.TotalOutputTokens != nil || o.DeadlineSeconds != nil || o.ShutdownOnCompletion != nil

	switch mode {
	case ModeInteractive:
		if hasBatch {
			return fmt.Errorf("interactive profile does not accept batch overrides")
		}
	case ModeBatch:
		if hasInteractive {
			return fmt.Errorf("batch profile does not accept interactive overrides")
		}
	default:
		return fmt.Errorf("unsupported mode %q", mode)
	}
	return nil
}
