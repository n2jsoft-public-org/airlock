package ui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

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

	l.frameIndex = (l.frameIndex + 1) % len(spinnerFrames)
	l.tickCount++
	if len(l.funnyLines) > 1 && l.tickCount%loadingPhraseInterval == 0 {
		l.phraseIndex = (l.phraseIndex + 1) % len(l.funnyLines)
	}

	return loadingTickCmd()
}

func (l loadingState) View(styles Styles, title string) string {
	return styles.Container.Render(styles.Title.Render(title) + "\n\n" + l.Body(styles))
}

// Body renders the spinner, label and current phrase without any title, for embedding in a frame.
func (l loadingState) Body(styles Styles) string {
	content := styles.Subtitle.Render(spinnerFrames[l.frameIndex] + " " + l.label)
	if len(l.funnyLines) > 0 {
		content += "\n" + styles.Muted.Render(l.funnyLines[l.phraseIndex])
	}
	return content
}
