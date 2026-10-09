package main

import (
	"fmt"
	"os"

	"github.com/n2jsoft/airlock/internal/cli"
	"github.com/n2jsoft/airlock/internal/i18n"
)

func main() {
	if err := cli.Execute(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, i18n.Localize(err)) //nolint:errcheck // nothing left to report a failure to.
		os.Exit(1)
	}
}
