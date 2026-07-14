package cli

import (
	"fmt"
	"io"
)

const help = "gpuquote commands: help, presets, workload\n"

func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		if _, err := io.WriteString(stdout, help); err != nil {
			_, _ = fmt.Fprintf(stderr, "write help: %v\n", err)
			return 1
		}
		return 0
	}

	switch args[0] {
	case "presets":
		return runPresets(args[1:], stdout, stderr)
	case "workload":
		return runWorkload(args[1:], stdout, stderr)
	default:
		_, _ = fmt.Fprintf(stderr, "unknown command %q\n", args[0])
		return 2
	}
}
