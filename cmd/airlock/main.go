package main

import (
	"fmt"
	"os"

	"github.com/n2jsoft/airlock/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err) //nolint:errcheck // nothing left to report a failure to.
		os.Exit(1)
	}
}
