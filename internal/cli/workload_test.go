package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
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

func TestRunWorkloadRejectsExplicitNegativeNumericOverrides(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		preset string
		flag   string
	}{
		{name: "concurrency", preset: "personal-chat", flag: "concurrency"},
		{name: "request rate", preset: "personal-chat", flag: "requests-per-second"},
		{name: "input tokens", preset: "personal-chat", flag: "input-tokens"},
		{name: "output tokens", preset: "personal-chat", flag: "output-tokens"},
		{name: "active hours", preset: "personal-chat", flag: "active-hours-per-day"},
		{name: "maximum TTFT", preset: "personal-chat", flag: "max-ttft-ms"},
		{name: "minimum decode rate", preset: "personal-chat", flag: "min-decode-tps"},
		{name: "total input tokens", preset: "batch-job", flag: "total-input-tokens"},
		{name: "total output tokens", preset: "batch-job", flag: "total-output-tokens"},
		{name: "deadline", preset: "batch-job", flag: "deadline-seconds"},
	}

	for _, test := range tests {
		for _, value := range []string{"-1", "-2"} {
			t.Run(test.name+" "+value, func(t *testing.T) {
				t.Parallel()

				var stdout bytes.Buffer
				var stderr bytes.Buffer
				code := Run([]string{"workload", "-preset", test.preset, "-" + test.flag + "=" + value}, &stdout, &stderr)
				if code != 2 {
					t.Fatalf("code = %d, want 2; stdout = %q; stderr = %q", code, stdout.String(), stderr.String())
				}
				if stdout.Len() != 0 {
					t.Errorf("stdout = %q, want empty", stdout.String())
				}
				wantStderr := fmt.Sprintf("workload: %s must be non-negative\n", test.flag)
				if stderr.String() != wantStderr {
					t.Errorf("stderr = %q, want %q", stderr.String(), wantStderr)
				}
			})
		}
	}
}

func TestRunWorkloadRejectsEmptyShutdownOverride(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run([]string{"workload", "-preset", "batch-job", "-shutdown-on-completion="}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("code = %d, want 2; stdout = %q; stderr = %q", code, stdout.String(), stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
	const wantStderr = "workload: shutdown-on-completion: strconv.ParseBool: parsing \"\": invalid syntax\n"
	if stderr.String() != wantStderr {
		t.Errorf("stderr = %q, want %q", stderr.String(), wantStderr)
	}
}

func TestRunRejectsUnsupportedOutputFormat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		args       []string
		wantStderr string
	}{
		{
			name:       "presets",
			args:       []string{"presets", "-output", "yaml"},
			wantStderr: "presets: unsupported output format \"yaml\"\n",
		},
		{
			name:       "workload before preset validation",
			args:       []string{"workload", "-output", "yaml"},
			wantStderr: "workload: unsupported output format \"yaml\"\n",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var stdout bytes.Buffer
			var stderr bytes.Buffer
			code := Run(test.args, &stdout, &stderr)
			if code != 2 {
				t.Fatalf("code = %d, want 2; stdout = %q; stderr = %q", code, stdout.String(), stderr.String())
			}
			if stdout.Len() != 0 {
				t.Errorf("stdout = %q, want empty", stdout.String())
			}
			if stderr.String() != test.wantStderr {
				t.Errorf("stderr = %q, want %q", stderr.String(), test.wantStderr)
			}
		})
	}
}

func TestRunWorkloadReportsWriterFailure(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer
	code := Run([]string{"workload", "-preset", "personal-chat"}, failingWriter{}, &stderr)
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	const wantStderr = "workload: write output: write failed\n"
	if stderr.String() != wantStderr {
		t.Fatalf("stderr = %q, want %q", stderr.String(), wantStderr)
	}
}
