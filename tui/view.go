package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/Naveen-6087/discv5-crawler/stats"
)

// Snapshot is everything one frame shows. It is built from the crawler's
// counters and the most recent network report.
type Snapshot struct {
	Elapsed     time.Duration
	Listen      string
	Known       int
	Queued      int64
	Contacted   int64
	Unreachable int64
	Requests    int64
	Buckets     int64
	Partial     int64
	Aborted     int64
	Discovered  int64
	Duplicates  int64
	Errors      map[string]int64
	NewPerSec   []float64 // most recent last
	ReqPerSec   float64
	Report      *stats.Report
	Done        bool
}

var (
	accent   = lipgloss.Color("#5FAFFF")
	good     = lipgloss.Color("#87D787")
	warn     = lipgloss.Color("#FFAF5F")
	muted    = lipgloss.Color("#8A8A8A")
	titleSty = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FFFFFF")).Background(lipgloss.Color("#005F87")).Padding(0, 1)
	boxSty   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#3A3A3A")).Padding(0, 1)
	headSty  = lipgloss.NewStyle().Bold(true).Foreground(accent)
	labelSty = lipgloss.NewStyle().Foreground(muted)
	numSty   = lipgloss.NewStyle().Bold(true)
)

const sparkChars = "▁▂▃▄▅▆▇█"

// Sparkline maps values onto eight block heights, scaled to the maximum.
func Sparkline(vals []float64, width int) string {
	if len(vals) > width {
		vals = vals[len(vals)-width:]
	}
	maxV := 0.0
	for _, v := range vals {
		maxV = max(maxV, v)
	}
	runes := []rune(sparkChars)
	var b strings.Builder
	for i := 0; i < width-len(vals); i++ {
		b.WriteRune(' ')
	}
	for _, v := range vals {
		idx := 0
		if maxV > 0 {
			idx = int(v / maxV * float64(len(runes)-1))
		}
		b.WriteRune(runes[idx])
	}
	return b.String()
}

// Bar is a horizontal bar of width w filled to frac.
func Bar(frac float64, w int) string {
	n := int(frac*float64(w) + 0.5)
	n = max(0, min(n, w))
	return strings.Repeat("█", n) + strings.Repeat("░", w-n)
}

func kv(label string, v any) string {
	return fmt.Sprintf("%s %s", labelSty.Render(fmt.Sprintf("%-12s", label)), numSty.Render(fmt.Sprint(v)))
}

func section(title string, lines []string, width, rows int) string {
	for len(lines) < rows {
		lines = append(lines, "")
	}
	body := headSty.Render(title) + "\n" + strings.Join(lines, "\n")
	return boxSty.Width(width).Render(body)
}

func countRows(cs []stats.Count, total, n, labelW, barW int) []string {
	if len(cs) == 0 {
		return []string{labelSty.Render("waiting for data…")}
	}
	var out []string
	for i, c := range cs {
		if i == n {
			break
		}
		name := c.Key
		if c.Label != "" {
			name = c.Label
		}
		if len(name) > labelW {
			name = name[:labelW-1] + "…"
		}
		frac := 0.0
		if total > 0 {
			frac = float64(c.N) / float64(total)
		}
		out = append(out, fmt.Sprintf("%-*s %s %5.1f%%", labelW, name, lipgloss.NewStyle().Foreground(accent).Render(Bar(frac, barW)), frac*100))
	}
	return out
}

// Render draws a frame for a terminal of the given width.
func Render(s Snapshot, width int) string {
	if width <= 0 {
		width = 120
	}
	status := lipgloss.NewStyle().Foreground(good).Render("● crawling")
	if s.Done {
		status = lipgloss.NewStyle().Foreground(warn).Render("■ finished")
	}
	header := lipgloss.JoinHorizontal(lipgloss.Center,
		titleSty.Render("discv5-crawler"), "  ", status,
		labelSty.Render(fmt.Sprintf("   elapsed %s   listening %s", s.Elapsed.Round(time.Second), s.Listen)))

	cols := 3
	if width < 110 {
		cols = 1
	}
	boxW := width/cols - 2
	if cols == 1 {
		boxW = width - 2
	}

	newRate := 0.0
	if n := len(s.NewPerSec); n > 0 {
		newRate = s.NewPerSec[n-1]
	}
	crawl := section("Crawl", []string{
		kv("known", s.Known),
		kv("queued", s.Queued),
		kv("contacted", s.Contacted),
		kv("unreachable", s.Unreachable),
		kv("requests", fmt.Sprintf("%d  (%.0f/s)", s.Requests, s.ReqPerSec)),
		kv("bucket qs", s.Buckets),
	}, boxW, 6)

	spark := lipgloss.NewStyle().Foreground(good).Render(Sparkline(s.NewPerSec, max(10, boxW-4)))
	discovery := section("Discovery (new nodes/s)", []string{
		spark,
		kv("now", fmt.Sprintf("%.0f/s", newRate)),
		kv("discovered", s.Discovered),
		kv("duplicates", s.Duplicates),
		kv("dup ratio", ratio(s.Duplicates, s.Discovered+s.Duplicates)),
	}, boxW, 6)

	errKeys := make([]string, 0, len(s.Errors))
	for k := range s.Errors {
		errKeys = append(errKeys, k)
	}
	sort.Strings(errKeys)
	if len(errKeys) > 4 {
		errKeys = errKeys[:4]
	}
	errLines := []string{kv("partial", s.Partial), kv("aborted", s.Aborted)}
	for _, k := range errKeys {
		errLines = append(errLines, lipgloss.NewStyle().Foreground(warn).Render(fmt.Sprintf("%-16s %d", k, s.Errors[k])))
	}
	errorsBox := section("Errors", errLines, boxW, 6)

	var forks, clients, custody []string
	resp := 0
	barW := max(10, min(24, boxW-28))
	if r := s.Report; r != nil {
		resp = r.Responsive
		forks = countRows(r.ForkDigests, r.Consensus, 6, 12, barW)
		clients = countRows(r.Clients, r.Counted, 6, 12, barW)
		custody = countRows(r.Custody, r.Consensus, 6, 12, barW)
	} else {
		forks = countRows(nil, 0, 0, 0, 0)
		clients, custody = forks, forks
	}
	forkBox := section(fmt.Sprintf("Fork digests (%d responsive)", resp), forks, boxW, 6)
	clientBox := section("Clients", clients, boxW, 6)
	custodyBox := section("PeerDAS custody groups", custody, boxW, 6)

	var body string
	if cols == 3 {
		body = lipgloss.JoinVertical(lipgloss.Left,
			lipgloss.JoinHorizontal(lipgloss.Top, crawl, discovery, errorsBox),
			lipgloss.JoinHorizontal(lipgloss.Top, forkBox, clientBox, custodyBox))
	} else {
		body = lipgloss.JoinVertical(lipgloss.Left, crawl, discovery, errorsBox, forkBox, clientBox, custodyBox)
	}
	footer := labelSty.Render("q quit   ·   network report refreshes every 3s   ·   database flushed periodically")
	return lipgloss.JoinVertical(lipgloss.Left, header, "", body, footer)
}

func ratio(a, b int64) string {
	if b == 0 {
		return "-"
	}
	return fmt.Sprintf("%.1f%%", 100*float64(a)/float64(b))
}
