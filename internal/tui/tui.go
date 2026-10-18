// Package tui is ctxtop's live view: it polls on an interval and redraws.
package tui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/iBvishal/ctxtop/internal/collect"
	"github.com/iBvishal/ctxtop/internal/snapshot"
)

type snapMsg snapshot.Snapshot

type tickMsg struct{}

// Model polls one round at a time: the next poll is scheduled only when the
// previous one has returned, so the collector is never used concurrently.
type Model struct {
	c        *collect.Collector
	interval time.Duration
	snap     snapshot.Snapshot
	have     bool
	width    int
	height   int
}

func New(c *collect.Collector, interval time.Duration) Model {
	return Model{c: c, interval: interval}
}

func (m Model) Init() tea.Cmd { return m.poll() }

func (m Model) poll() tea.Cmd {
	c := m.c
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return snapMsg(c.Snapshot(ctx))
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			return m, tea.Quit
		case "r":
			m.c.Rescan()
		}
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case snapMsg:
		m.snap, m.have = snapshot.Snapshot(msg), true
		return m, tea.Tick(m.interval, func(time.Time) tea.Msg { return tickMsg{} })
	case tickMsg:
		return m, m.poll()
	}
	return m, nil
}

func (m Model) View() tea.View {
	v := tea.NewView(Render(m.snap, m.have, m.width, m.height, m.interval))
	v.AltScreen = true
	return v
}
