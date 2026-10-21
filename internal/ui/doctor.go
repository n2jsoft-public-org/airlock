package ui

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/n2jsoft/airlock/internal/doctor"
	"golang.org/x/term"
)

// ErrDoctorCanceled is returned by ShowDoctor when the user leaves before every check answered.
var ErrDoctorCanceled = errors.New("doctor canceled")

// DoctorSection is a group of checks airlock doctor shows under Title; sections run concurrently.
type DoctorSection struct {
	Run   func() []doctor.Result
	Title string
}

// CheckDoctor runs the sections concurrently and returns their results in the sections' order.
func CheckDoctor(sections []DoctorSection) []doctor.Result {
	return slices.Concat(runDoctorSections(sections)...)
}

func runDoctorSections(sections []DoctorSection) [][]doctor.Result {
	results := make([][]doctor.Result, len(sections))
	var wg sync.WaitGroup
	for i, section := range sections {
		wg.Go(func() { results[i] = section.Run() })
	}
	wg.Wait()
	return results
}

// ShowDoctor runs the sections concurrently and prints each one, in order, as soon as it and the
// ones before it answered, with a spinner for those still running, then the counts. On anything
// but two terminals it prints everything once all answered.
func ShowDoctor(sections []DoctorSection, in io.Reader, out io.Writer) ([]doctor.Result, error) {
	inFile, inOK := in.(*os.File)
	outFile, outOK := out.(*os.File)
	if !inOK || !outOK || !term.IsTerminal(int(inFile.Fd())) || !term.IsTerminal(int(outFile.Fd())) {
		start := time.Now()
		results := runDoctorSections(sections)
		view := newDoctorView(NewStyles(true), sections)
		var b strings.Builder
		for i := range sections {
			b.WriteString(view.renderSection(i, results[i]))
		}
		b.WriteString(view.renderSummary(slices.Concat(results...), time.Since(start)) + "\n")
		_, err := lipgloss.Fprint(out, b.String())
		return slices.Concat(results...), err
	}
	program := tea.NewProgram(newDoctorView(NewStyles(lipgloss.HasDarkBackground(inFile, outFile)), sections),
		tea.WithInput(inFile), tea.WithOutput(outFile))
	final, err := program.Run()
	view, ok := final.(doctorView)
	switch {
	case errors.Is(err, tea.ErrInterrupted) || (ok && view.canceled):
		return nil, ErrDoctorCanceled
	case err != nil:
		return nil, err
	}
	return slices.Concat(view.results...), nil
}

type doctorSectionDoneMsg struct {
	results []doctor.Result
	index   int
}

// doctorView prints the answered sections above itself and shows the running ones.
type doctorView struct {
	start    time.Time
	styles   Styles
	sections []DoctorSection
	results  [][]doctor.Result
	answered []bool
	// printed is how many sections, from the first, were printed.
	printed  int
	frame    int
	width    int
	elapsed  time.Duration
	canceled bool
}

func newDoctorView(styles Styles, sections []DoctorSection) doctorView {
	return doctorView{styles: styles, sections: sections, results: make([][]doctor.Result, len(sections)),
		answered: make([]bool, len(sections)), start: time.Now()}
}

func (v doctorView) Init() tea.Cmd {
	cmds := []tea.Cmd{loadingTickCmd()}
	for i, section := range v.sections {
		cmds = append(cmds, func() tea.Msg { return doctorSectionDoneMsg{index: i, results: section.Run()} })
	}
	return tea.Batch(cmds...)
}

func (v doctorView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		v.width = msg.Width
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			v.canceled = true
			return v, tea.Quit
		}
	case loadingTickMsg:
		if v.done() {
			return v, nil
		}
		v.frame = (v.frame + 1) % len(spinnerFrames)
		return v, loadingTickCmd()
	case doctorSectionDoneMsg:
		v.results[msg.index], v.answered[msg.index] = msg.results, true
		var b strings.Builder
		for ; v.printed < len(v.sections) && v.answered[v.printed]; v.printed++ {
			b.WriteString(v.renderSection(v.printed, v.results[v.printed]))
		}
		if b.Len() == 0 {
			return v, nil
		}
		if !v.done() {
			return v, tea.Sequence(printLines(strings.TrimSuffix(b.String(), "\n"))...)
		}
		v.elapsed = time.Since(v.start)
		b.WriteString(v.renderSummary(slices.Concat(v.results...), v.elapsed))
		return v, tea.Sequence(append(printLines(b.String()), tea.Quit)...)
	}
	return v, nil
}

// printLines prints text above the view one line at a time: Bubble Tea leaves the view behind when
// it prints more lines at once than the terminal is high.
func printLines(text string) []tea.Cmd {
	lines := strings.Split(text, "\n")
	cmds := make([]tea.Cmd, 0, len(lines))
	for _, line := range lines {
		cmds = append(cmds, tea.Println(cmp.Or(line, " "))) // Bubble Tea prints no empty line
	}
	return cmds
}

func (v doctorView) done() bool { return v.printed == len(v.sections) }

func (v doctorView) View() tea.View {
	if v.canceled || v.done() {
		return tea.NewView("") // done: the summary was printed with the last sections
	}
	lines := make([]string, 0, len(v.sections)-v.printed)
	for i := v.printed; i < len(v.sections); i++ {
		mark := v.styles.Faint.Render("•") // answered, printed once the sections before it are
		if !v.answered[i] {
			mark = v.styles.Title.Render(spinnerFrames[v.frame])
		}
		lines = append(lines, mark+" "+v.styles.Muted.Render(v.sections[i].Title))
	}
	return tea.NewView(strings.Join(lines, "\n"))
}

// renderSection is the title of section i and one line per result, its fix under it, then a
// blank line.
func (v doctorView) renderSection(i int, results []doctor.Result) string {
	var b strings.Builder
	b.WriteString(v.styles.Subtitle.Render(v.sections[i].Title) + "\n")
	for j := range results {
		r := &results[j]
		b.WriteString("  " + v.mark(r.Status) + " " + v.styles.Text.Render(v.wrap(r.Message, 4)) + "\n")
		if r.Fix != "" {
			b.WriteString("    " + v.styles.Faint.Render("→ "+v.wrap(r.Fix, 6)) + "\n")
		}
	}
	return b.String() + "\n"
}

// wrap breaks text to the terminal width, the lines after the first indented by indent columns:
// Bubble Tea miscounts the lines printed above it when the terminal wraps them itself.
func (v doctorView) wrap(text string, indent int) string {
	if v.width <= indent+20 {
		return text
	}
	return strings.ReplaceAll(ansi.Wrap(text, v.width-indent, ""), "\n", "\n"+strings.Repeat(" ", indent))
}

func (v doctorView) mark(status doctor.Status) string {
	switch status {
	case doctor.Failure:
		return v.styles.Danger.Render("✗")
	case doctor.Warning:
		return v.styles.Warning.Render("!")
	default:
		return v.styles.Success.Render("✓")
	}
}

// renderSummary counts the results per status, then the time the checks took.
func (v doctorView) renderSummary(results []doctor.Result, elapsed time.Duration) string {
	counts := map[doctor.Status]int{}
	for i := range results {
		counts[results[i].Status]++
	}
	parts := []string{v.mark(doctor.OK) + " " + fmt.Sprint(counts[doctor.OK])}
	for _, status := range []doctor.Status{doctor.Warning, doctor.Failure} {
		if counts[status] > 0 {
			parts = append(parts, v.mark(status)+" "+fmt.Sprint(counts[status]))
		}
	}
	return strings.Join(parts, "  ") + v.styles.Faint.Render(" · "+elapsed.Round(100*time.Millisecond).String())
}
