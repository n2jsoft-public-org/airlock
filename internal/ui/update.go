package ui

import (
	"cmp"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"golang.org/x/term"
)

// ReleaseNotes is the tag of a release and its notes, in the Markdown semantic-release writes.
type ReleaseNotes struct {
	Tag  string
	Body string
}

// UpdateOutput prints the steps of airlock update: a spinner while a step runs on a terminal, then
// one line per step with its outcome.
type UpdateOutput struct {
	out    io.Writer
	styles Styles
	live   bool
}

// NewUpdateOutput prints to out; the spinner and the colors need in and out to be terminals.
func NewUpdateOutput(in io.Reader, out io.Writer) *UpdateOutput {
	inFile, inOK := in.(*os.File)
	outFile, outOK := out.(*os.File)
	if !inOK || !outOK || !term.IsTerminal(int(inFile.Fd())) || !term.IsTerminal(int(outFile.Fd())) {
		return &UpdateOutput{out: out, styles: NewStyles(true)}
	}
	return &UpdateOutput{out: out, styles: NewStyles(lipgloss.HasDarkBackground(inFile, outFile)), live: true}
}

// Step runs run with a spinner beside label, then prints ✓ and the text run returns (label when
// empty), or ✗ and label when it fails.
func (u *UpdateOutput) Step(label string, run func() (string, error)) error {
	stop := u.spin(label)
	done, err := run()
	stop()
	if err != nil {
		u.println(u.styles.Danger.Render("✗") + " " + u.styles.Text.Render(label))
		return err
	}
	u.println(u.styles.Success.Render("✓") + " " + u.styles.Text.Render(cmp.Or(done, label)))
	return nil
}

func (u *UpdateOutput) spin(label string) (stop func()) {
	if !u.live {
		return func() {}
	}
	quit, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(loadingTickInterval)
		defer ticker.Stop()
		for frame := 0; ; frame = (frame + 1) % len(spinnerFrames) {
			u.print("\r" + ansi.EraseEntireLine + u.styles.Title.Render(spinnerFrames[frame]) + " " + u.styles.Muted.Render(label))
			select {
			case <-quit:
				u.print("\r" + ansi.EraseEntireLine)
				return
			case <-ticker.C:
			}
		}
	}()
	return func() { close(quit); <-stopped }
}

// Done prints the outcome of the update, highlighted.
func (u *UpdateOutput) Done(text string) {
	u.println(u.styles.Success.Render("✓ " + text))
}

// Note prints something left for the user to do.
func (u *UpdateOutput) Note(text string) {
	u.println(u.styles.Warning.Render("!") + " " + u.styles.Text.Render(text))
}

// Changes prints title then the notes of each release, in the order given; nothing when no release
// has notes.
func (u *UpdateOutput) Changes(title string, releases []ReleaseNotes) {
	var b strings.Builder
	for _, r := range releases {
		lines := u.notesLines(r.Body)
		if len(lines) == 0 {
			continue // a release of refactors or tests only
		}
		b.WriteString("\n" + u.styles.Title.Render(r.Tag))
		for _, line := range lines {
			b.WriteString("\n" + line)
		}
		b.WriteString("\n")
	}
	if b.Len() > 0 {
		u.println("\n" + u.styles.Subtitle.Render(title) + "\n" + b.String())
	}
}

var (
	// commitLink is the ([sha](url)) semantic-release appends to each change.
	commitLink   = regexp.MustCompile(`\s*\(\[[0-9a-f]{7,40}\]\([^)]*\)\)`)
	markdownLink = regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`)
	boldText     = regexp.MustCompile(`\*\*([^*]+)\*\*`)
)

// notesLines turns release notes into indented terminal lines: the sections under their title,
// one bullet per change without its links, the release header and blank lines left out.
func (u *UpdateOutput) notesLines(body string) []string {
	var lines []string
	for line := range strings.SplitSeq(body, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "", strings.HasPrefix(line, "# "), strings.HasPrefix(line, "## "):
		case strings.HasPrefix(line, "### "):
			lines = append(lines, "  "+u.styles.Subtitle.Render(strings.TrimPrefix(line, "### ")))
		case strings.HasPrefix(line, "* "), strings.HasPrefix(line, "- "):
			lines = append(lines, "    "+u.styles.Faint.Render("•")+" "+u.inline(line[2:]))
		default:
			lines = append(lines, "    "+u.inline(line))
		}
	}
	return lines
}

func (u *UpdateOutput) inline(text string) string {
	text = markdownLink.ReplaceAllString(commitLink.ReplaceAllString(text, ""), "$1")
	return boldText.ReplaceAllStringFunc(text, func(s string) string {
		return u.styles.Muted.Render(strings.Trim(s, "*"))
	})
}

func (u *UpdateOutput) print(s string) {
	_, _ = lipgloss.Fprint(u.out, s) //nolint:errcheck // nothing to do when the terminal is gone
}

func (u *UpdateOutput) println(s string) {
	_, _ = lipgloss.Fprintln(u.out, s) //nolint:errcheck // nothing to do when the terminal is gone
}
