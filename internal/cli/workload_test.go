package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/iuliandita/gpuquote/internal/workload"
)

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}

func TestRunPresetsJSON(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run([]string{"presets", "-output", "json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d; stderr = %q", code, stderr.String())
	}

	var profiles []workload.Profile
	if err := json.Unmarshal(stdout.Bytes(), &profiles); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if len(profiles) != 4 || profiles[0].Preset != "batch-job" || profiles[3].Preset != "personal-chat" {
		t.Fatalf("profiles = %+v", profiles)
	}
}

func TestRunWorkloadExpandsAndOverridesPreset(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run([]string{
		"workload",
		"-preset", "coding-agent",
		"-concurrency", "4",
		"-input-tokens", "4096",
		"-output", "json",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d; stderr = %q", code, stderr.String())
	}

	var profile workload.Profile
	if err := json.Unmarshal(stdout.Bytes(), &profile); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if profile.Preset != "coding-agent" || profile.PresetVersion != 1 || profile.ConcurrentRequests != 4 || profile.InputTokens != 4096 {
		t.Fatalf("profile = %+v", profile)
	}
}

func TestRunWorkloadRejectsCrossModeOverride(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run([]string{"workload", "-preset", "batch-job", "-concurrency", "1"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	if stderr.Len() == 0 {
		t.Fatal("stderr is empty")
	}
}

func TestRunWorkloadRetainsFalseShutdownOverrideInJSON(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run([]string{"workload", "-preset", "batch-job", "-shutdown-on-completion", "false", "-output", "json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d; stderr = %q", code, stderr.String())
	}
	const want = "{\"preset\":\"batch-job\",\"preset_version\":1,\"mode\":\"batch\",\"total_input_tokens\":1000000,\"total_output_tokens\":250000,\"deadline_seconds\":14400,\"shutdown_on_completion\":false}\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
}

func TestRunWorkloadReportsWriterFailure(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer
	code := Run([]string{"workload", "-preset", "personal-chat"}, failingWriter{}, &stderr)
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
}
