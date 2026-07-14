package cli

import (
	"bytes"
	"testing"
)

func TestRunHelp(t *testing.T) {
	const wantHelp = "gpuquote commands: help, presets, workload\n"

	tests := []struct {
		name string
		args []string
	}{
		{name: "no arguments", args: nil},
		{name: "help", args: []string{"help"}},
		{name: "short help", args: []string{"-h"}},
		{name: "long help", args: []string{"--help"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer

			if got := Run(test.args, &stdout, &stderr); got != 0 {
				t.Fatalf("Run() code = %d, want 0; stderr = %q", got, stderr.String())
			}
			if got := stdout.String(); got != wantHelp {
				t.Errorf("Run() stdout = %q, want %q; stderr = %q", got, wantHelp, stderr.String())
			}
			if got := stderr.String(); got != "" {
				t.Errorf("Run() stderr = %q, want empty", got)
			}
		})
	}
}

func TestRunRejectsUnknownCommand(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	if got := Run([]string{"unknown"}, &stdout, &stderr); got != 2 {
		t.Fatalf("Run() code = %d, want 2; stderr = %q", got, stderr.String())
	}

	const wantStderr = "unknown command \"unknown\"\n"
	if got := stderr.String(); got != wantStderr {
		t.Errorf("Run() stderr = %q, want %q", got, wantStderr)
	}
	if got := stdout.String(); got != "" {
		t.Errorf("Run() stdout = %q, want empty", got)
	}
}
