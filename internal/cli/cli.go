package cli

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

const help = "gpuquote commands: help, presets, workload, offers\n"

func Run(args []string, stdout, stderr io.Writer) int {
	return run(args, stdout, stderr, dependencies{
		getenv:      os.Getenv,
		httpClient:  &http.Client{},
		now:         func() time.Time { return time.Now().UTC() },
		newAdapters: newProviderAdapters,
	})
}

func run(args []string, stdout, stderr io.Writer, deps dependencies) int {
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
	case "offers":
		return runOffers(args[1:], stdout, stderr, deps)
	default:
		_, _ = fmt.Fprintf(stderr, "unknown command %q\n", args[0])
		return 2
	}
}
