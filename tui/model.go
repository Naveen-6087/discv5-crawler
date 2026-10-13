package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Naveen-6087/discv5-crawler/crawler"
	"github.com/Naveen-6087/discv5-crawler/nodedb"
	"github.com/Naveen-6087/discv5-crawler/stats"
)

const (
	tickEvery     = time.Second
	reportEvery   = 3
	historyLength = 120
)

type tickMsg time.Time

type reportMsg struct{ r stats.Report }

// DoneMsg tells the dashboard that the crawl has stopped.
type DoneMsg struct{ Err error }

// Model is the Bubble Tea model of a live crawl.
type Model struct {
	stats     *crawler.Stats
	db        *nodedb.DB
	listen    string
	start     time.Time
	cancel    context.CancelFunc
	width     int
	ticks     int
	lastDisc  int64
	lastReqs  int64
	history   []float64
	reqRate   float64
	report    *stats.Report
	computing bool
	done      bool
}

func New(s *crawler.Stats, db *nodedb.DB, listen string, cancel context.CancelFunc) Model {
	return Model{stats: s, db: db, listen: listen, start: time.Now(), cancel: cancel}
}

func tick() tea.Cmd {
	return tea.Tick(tickEvery, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m Model) computeReport() tea.Cmd {
	db := m.db
	return func() tea.Msg {
		return reportMsg{stats.Compute(db.Entries(), time.Now(), true)}
	}
}

func (m Model) Init() tea.Cmd {
	return tick()
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			if m.cancel != nil {
				m.cancel()
			}
			return m, tea.Quit
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
	case tickMsg:
		m.ticks++
		disc, reqs := m.stats.Discovered.Load(), m.stats.Requests.Load()
		m.history = append(m.history, float64(disc-m.lastDisc))
		if len(m.history) > historyLength {
			m.history = m.history[len(m.history)-historyLength:]
		}
		m.reqRate = float64(reqs - m.lastReqs)
		m.lastDisc, m.lastReqs = disc, reqs
		cmds := []tea.Cmd{tick()}
		if !m.computing && m.ticks%reportEvery == 1 {
			m.computing = true
			cmds = append(cmds, m.computeReport())
		}
		return m, tea.Batch(cmds...)
	case reportMsg:
		r := msg.r
		m.report = &r
		m.computing = false
	case DoneMsg:
		m.done = true
		return m, m.computeReport()
	}
	return m, nil
}

// Snapshot captures the current counters for rendering.
func (m Model) Snapshot() Snapshot {
	s := m.stats
	return Snapshot{
		Elapsed:     time.Since(m.start),
		Listen:      m.listen,
		Known:       m.db.Len(),
		Queued:      s.Queued.Load(),
		Contacted:   s.Contacted.Load(),
		Unreachable: s.Unreachable.Load(),
		Requests:    s.Requests.Load(),
		Buckets:     s.BucketQueries.Load(),
		Partial:     s.Partial.Load(),
		Aborted:     s.SweepAborted.Load(),
		Discovered:  s.Discovered.Load(),
		Duplicates:  s.Duplicates.Load(),
		Errors:      s.Errors.Snapshot(),
		NewPerSec:   m.history,
		ReqPerSec:   m.reqRate,
		Report:      m.report,
		Done:        m.done,
	}
}

func (m Model) View() string {
	return Render(m.Snapshot(), m.width)
}
