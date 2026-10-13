package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Naveen-6087/discv5-crawler/crawler"
	"github.com/Naveen-6087/discv5-crawler/nodedb"
	"github.com/Naveen-6087/discv5-crawler/stats"
)

func TestSparkline(t *testing.T) {
	if got := Sparkline([]float64{0, 1, 2, 4}, 6); got != "  ▁▂▄█" {
		t.Fatalf("sparkline %q", got)
	}
	if got := Sparkline(nil, 3); got != "   " {
		t.Fatalf("empty %q", got)
	}
	if got := []rune(Sparkline(make([]float64, 50), 10)); len(got) != 10 {
		t.Fatalf("width %d", len(got))
	}
}

func TestBar(t *testing.T) {
	if Bar(0.5, 10) != "█████░░░░░" || Bar(2, 4) != "████" || Bar(-1, 3) != "░░░" {
		t.Fatal("bar")
	}
}

func TestRenderWideAndNarrow(t *testing.T) {
	s := Snapshot{
		Elapsed: 83 * time.Second, Listen: "0.0.0.0:30303", Known: 12345, Contacted: 678,
		Requests: 9000, ReqPerSec: 250, Discovered: 12000, Duplicates: 3000,
		Errors: map[string]int64{"timeout": 42}, NewPerSec: []float64{1, 5, 9},
		Report: &stats.Report{
			Responsive: 600, Consensus: 400, Counted: 600,
			ForkDigests: []stats.Count{{Key: "8c9f62fe", Label: "fulu-bpo2", N: 300}},
			Clients:     []stats.Count{{Key: "Lighthouse", N: 120}},
			Custody:     []stats.Count{{Key: "128", N: 90}},
		},
	}
	for _, w := range []int{160, 80} {
		out := Render(s, w)
		for _, want := range []string{"discv5-crawler", "12345", "678", "250/s", "fulu-bpo2", "Lighthouse", "timeout", "1m23s", "75.0%"} {
			if !strings.Contains(out, want) {
				t.Fatalf("width %d: missing %q in\n%s", w, want, out)
			}
		}
	}
	s.Done = true
	if !strings.Contains(Render(s, 160), "finished") {
		t.Fatal("finished state not shown")
	}
	s.Report = nil
	if !strings.Contains(Render(s, 160), "waiting for data") {
		t.Fatal("placeholder not shown before the first report")
	}
}

func TestModelUpdates(t *testing.T) {
	var st crawler.Stats
	db, _ := nodedb.Open("")
	cancelled := false
	m := New(&st, db, "127.0.0.1:1", func() { cancelled = true })

	st.Discovered.Add(10)
	st.Requests.Add(40)
	next, cmd := m.Update(tickMsg(time.Now()))
	m = next.(Model)
	if cmd == nil || len(m.history) != 1 || m.history[0] != 10 || m.reqRate != 40 || !m.computing {
		t.Fatalf("after tick: history %v rate %v computing %v", m.history, m.reqRate, m.computing)
	}
	next, _ = m.Update(reportMsg{stats.Report{Total: 3}})
	m = next.(Model)
	if m.report == nil || m.report.Total != 3 || m.computing {
		t.Fatal("report not stored")
	}
	next, _ = m.Update(DoneMsg{})
	if !next.(Model).done {
		t.Fatal("done not recorded")
	}
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if !cancelled || cmd == nil {
		t.Fatal("q did not cancel the crawl")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("q did not quit")
	}
}
