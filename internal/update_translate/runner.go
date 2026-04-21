package updatetranslate

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	translateRepo  = "example/translate"
	translateAsset = "i18n_acme.zip"
)

// Options controls update-translate execution.
type Options struct {
	Destination       string
	OverwriteExisting bool
}

// RunnerOptions controls runner I/O behavior.
type RunnerOptions struct {
	Out io.Writer
	Err io.Writer
}

// Runner downloads and extracts translation assets.
type Runner struct {
	out io.Writer
	err io.Writer
}

// NewRunner builds a runner with sane defaults.
func NewRunner(options RunnerOptions) Runner {
	out := options.Out
	if out == nil {
		out = io.Discard
	}

	errWriter := options.Err
	if errWriter == nil {
		errWriter = io.Discard
	}

	return Runner{out: out, err: errWriter}
}

// Run downloads the latest release asset and extracts it to destination.
func (r Runner) Run(ctx context.Context, options Options) error {
	destination, err := resolveDestination(options.Destination)
	if err != nil {
		return err
	}

	if ensureErr := ensureDestinationDir(destination); ensureErr != nil {
		return ensureErr
	}

	if _, lookPathErr := exec.LookPath("gh"); lookPathErr != nil {
		return fmt.Errorf("gh CLI is required: install GitHub CLI and run 'gh auth login': %w", lookPathErr)
	}

	tmpDir, err := os.MkdirTemp("", "acme-translate-*")
	if err != nil {
		return fmt.Errorf("create temp directory: %w", err)
	}
	defer func() {
		if removeErr := os.RemoveAll(tmpDir); removeErr != nil {
			if _, writeErr := fmt.Fprintf(r.err, "warning: failed to cleanup temp dir %s: %v\n", tmpDir, removeErr); writeErr != nil {
				return
			}
		}
	}()

	if _, err := fmt.Fprintf(r.out, "Downloading %s from latest release of %s...\n", translateAsset, translateRepo); err != nil {
		return fmt.Errorf("write output: %w", err)
	}

	if err := downloadLatestAsset(ctx, tmpDir); err != nil {
		return err
	}

	zipPath := filepath.Join(tmpDir, translateAsset)
	if _, err := os.Stat(zipPath); err != nil {
		return fmt.Errorf("download completed but %s is missing: %w", translateAsset, err)
	}

	if _, err := fmt.Fprintf(r.out, "Extracting archive into %s...\n", destination); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	if err := extractZip(zipPath, destination, options.OverwriteExisting); err != nil {
		return err
	}

	if _, err := fmt.Fprintln(r.out, "Translation update completed."); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

func resolveDestination(destination string) (string, error) {
	value := strings.TrimSpace(destination)
	if value == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve destination from current directory: %w", err)
		}
		value = cwd
	}

	absPath, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("resolve destination %q: %w", value, err)
	}

	return absPath, nil
}

func ensureDestinationDir(destination string) error {
	info, err := os.Stat(destination)
	if err == nil {
		if !info.IsDir() {
			return fmt.Errorf("destination %s is not a directory", destination)
		}
		return nil
	}

	if !os.IsNotExist(err) {
		return fmt.Errorf("check destination %s: %w", destination, err)
	}

	if err := os.MkdirAll(destination, 0o750); err != nil {
		return fmt.Errorf("create destination %s: %w", destination, err)
	}

	return nil
}

func downloadLatestAsset(ctx context.Context, destination string) error {
	args := []string{
		"release", "download",
		"--repo", translateRepo,
		"--pattern", translateAsset,
		"--dir", destination,
		"--clobber",
	}

	// #nosec G204 -- command and arguments are fixed except validated destination path.
	cmd := exec.CommandContext(ctx, "gh", args...)
	output, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}

	details := strings.TrimSpace(string(output))
	if details == "" {
		details = err.Error()
	}

	return fmt.Errorf("download latest release asset via gh failed (ensure 'gh auth login' can access private repo %s): %s", translateRepo, details)
}
