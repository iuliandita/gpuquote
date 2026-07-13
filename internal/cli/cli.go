package cli

import (
	"fmt"
	"io"
)

const help = "gpuquote commands: help\n"

func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		if _, err := fmt.Fprint(stdout, help); err != nil {
			fmt.Fprintf(stderr, "write help: %v\n", err)
			return 1
		}
		return 0
	}

	fmt.Fprintf(stderr, "unknown command %q\n", args[0])
	return 2
}
