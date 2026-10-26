package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/n2jsoft-public-org/airlock/internal/cli"
	"github.com/n2jsoft-public-org/airlock/internal/i18n"
)

func main() {
	if err := cli.Execute(); err != nil {
		// airlock exec ends with the exit status of its command, which said what went wrong.
		if exit, ok := errors.AsType[*cli.ExitCodeError](err); ok {
			os.Exit(exit.Code)
		}
		_, _ = fmt.Fprintln(os.Stderr, i18n.Localize(err)) //nolint:errcheck // nothing left to report a failure to.
		os.Exit(1)
	}
}
