package ui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

var loadingFrames = []string{"|", "/", "-", "\\"}

const (
	loadingTickInterval   = 120 * time.Millisecond
	loadingPhraseInterval = 18
)

type loadingTickMsg time.Time

type loadingState struct {
	active      bool
	label       string
	funnyLines  []string
	frameIndex  int
	phraseIndex int
	tickCount   int
}

func loadingTickCmd() tea.Cmd {
	return tea.Tick(loadingTickInterval, func(t time.Time) tea.Msg {
		return loadingTickMsg(t)
	})
}

func (l *loadingState) start(label string, funnyLines []string) tea.Cmd {
	l.active = true
	l.label = label
	l.funnyLines = append([]string(nil), funnyLines...)
	l.frameIndex = 0
	l.phraseIndex = 0
	l.tickCount = 0
	return loadingTickCmd()
}

func (l *loadingState) stop() {
	l.active = false
}

func (l *loadingState) update(msg tea.Msg) tea.Cmd {
	if !l.active {
		return nil
	}

	if _, ok := msg.(loadingTickMsg); !ok {
		return nil
	}

	l.frameIndex = (l.frameIndex + 1) % len(loadingFrames)
	l.tickCount++
	if len(l.funnyLines) > 1 && l.tickCount%loadingPhraseInterval == 0 {
		l.phraseIndex = (l.phraseIndex + 1) % len(l.funnyLines)
	}

	return loadingTickCmd()
}

func (l loadingState) View(styles Styles, title string) string {
	line := ""
	if len(l.funnyLines) > 0 {
		line = l.funnyLines[l.phraseIndex]
	}

	content := styles.Title.Render(title) + "\n\n" +
		styles.Subtitle.Render(loadingFrames[l.frameIndex]+" "+l.label)
	if line != "" {
		content += "\n" + styles.Muted.Render(line)
	}

	return styles.Container.Render(content)
}
