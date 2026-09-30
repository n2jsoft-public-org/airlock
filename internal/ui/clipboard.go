package ui

import (
	"io"
	"os"
	"strings"

	"github.com/atotto/clipboard"
	"github.com/aymanbagabas/go-osc52/v2"
)

// copyToClipboard puts text on the system clipboard of this machine (pbcopy, xclip, wl-copy...),
// else, over SSH or without a clipboard tool, on the terminal's own through OSC 52.
func copyToClipboard(text string) error {
	if !overSSH() && !clipboard.Unsupported {
		if err := clipboard.WriteAll(text); err == nil {
			return nil
		}
	}
	return writeOSC52(os.Stderr, text)
}

// overSSH reports a remote session, whose system clipboard is not the user's.
func overSSH() bool {
	return os.Getenv("SSH_TTY") != "" || os.Getenv("SSH_CONNECTION") != ""
}

// writeOSC52 asks the terminal to copy text, passing the request through tmux or screen.
func writeOSC52(w io.Writer, text string) error {
	seq := osc52.New(text)
	switch {
	case os.Getenv("TMUX") != "":
		seq = seq.Tmux()
	case strings.HasPrefix(os.Getenv("TERM"), "screen"):
		seq = seq.Screen()
	}
	_, err := seq.WriteTo(w)
	return err
}
