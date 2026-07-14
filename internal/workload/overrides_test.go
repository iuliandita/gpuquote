package workload

import "testing"

func TestOverridesApplyInteractiveFields(t *testing.T) {
	t.Parallel()

	base, err := Resolve("coding-agent")
	if err != nil {
		t.Fatal(err)
	}
	concurrency := 4
	inputTokens := int64(4096)
	profile, err := (Overrides{
		ConcurrentRequests: &concurrency,
		InputTokens:        &inputTokens,
	}).Apply(base)
	if err != nil {
		t.Fatalf("Overrides.Apply() error = %v", err)
	}
	if profile.ConcurrentRequests != 4 || profile.InputTokens != 4096 {
		t.Fatalf("profile = %+v", profile)
	}
	if profile.Preset != base.Preset || profile.PresetVersion != base.PresetVersion {
		t.Fatal("override changed preset identity")
	}
}

func TestOverridesRejectCrossModeFields(t *testing.T) {
	t.Parallel()

	batch, err := Resolve("batch-job")
	if err != nil {
		t.Fatal(err)
	}
	concurrency := 1
	if _, err := (Overrides{ConcurrentRequests: &concurrency}).Apply(batch); err == nil {
		t.Fatal("batch concurrency override error = nil")
	}
	zeroConcurrency := 0
	if _, err := (Overrides{ConcurrentRequests: &zeroConcurrency}).Apply(batch); err == nil {
		t.Fatal("zero-valued batch concurrency override error = nil")
	}

	interactive, err := Resolve("personal-chat")
	if err != nil {
		t.Fatal(err)
	}
	total := int64(100)
	if _, err := (Overrides{TotalInputTokens: &total}).Apply(interactive); err == nil {
		t.Fatal("interactive total-token override error = nil")
	}
	shutdown := false
	if _, err := (Overrides{ShutdownOnCompletion: &shutdown}).Apply(interactive); err == nil {
		t.Fatal("interactive shutdown override error = nil")
	}
}

func TestOverridesCanChangeBatchShutdownBehavior(t *testing.T) {
	t.Parallel()

	base, err := Resolve("batch-job")
	if err != nil {
		t.Fatal(err)
	}
	shutdown := false
	profile, err := (Overrides{ShutdownOnCompletion: &shutdown}).Apply(base)
	if err != nil {
		t.Fatalf("Overrides.Apply() error = %v", err)
	}
	if profile.ShutdownOnCompletion {
		t.Fatal("shutdown_on_completion = true, want false")
	}
}
