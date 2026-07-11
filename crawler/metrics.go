package crawler

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
)

var (
	errNoEndpoint = errors.New("record has no UDP endpoint")
	errWrongID    = errors.New("node answered with a different identity")
)

// Error classes reported in Stats.
const (
	ClassTimeout   = "timeout"
	ClassHandshake = "handshake"
	ClassClosed    = "closed"
	ClassInvalid   = "invalid response"
	ClassOther     = "other"
)

// Classify maps a discv5 error to a coarse class. go-ethereum does not export
// its error values, so this matches on their messages.
func Classify(err error) string {
	msg := err.Error()
	switch {
	case errors.Is(err, context.DeadlineExceeded) || strings.Contains(msg, "timeout"):
		return ClassTimeout
	case strings.Contains(msg, "handshake") || strings.Contains(msg, "WHOAREYOU"):
		return ClassHandshake
	case strings.Contains(msg, "closed"):
		return ClassClosed
	case strings.Contains(msg, "nodes in response") || strings.Contains(msg, "invalid") || errors.Is(err, errWrongID):
		return ClassInvalid
	default:
		return ClassOther
	}
}

// Stats are live counters for one crawl.
type Stats struct {
	Queued        atomic.Int64
	Contacted     atomic.Int64
	Unreachable   atomic.Int64
	NoEndpoint    atomic.Int64
	Requests      atomic.Int64
	Retries       atomic.Int64
	BucketQueries atomic.Int64 // successful FINDNODE requests
	SweepAborted  atomic.Int64 // sweeps cut short by a failed FINDNODE
	Partial       atomic.Int64 // FINDNODE replies with lost trailing packets
	Records       atomic.Int64 // records received in NODES replies
	Discovered    atomic.Int64 // of which new node IDs
	Updated       atomic.Int64 // of which newer records of known nodes
	Duplicates    atomic.Int64 // of which nothing new
	Errors        ErrorCounts
}

type ErrorCounts struct {
	mu sync.Mutex
	m  map[string]int64
}

func (e *ErrorCounts) record(err error) string {
	class := Classify(err)
	e.mu.Lock()
	if e.m == nil {
		e.m = map[string]int64{}
	}
	e.m[class]++
	e.mu.Unlock()
	return class
}

// Snapshot returns a copy of the counts by class.
func (e *ErrorCounts) Snapshot() map[string]int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make(map[string]int64, len(e.m))
	for k, v := range e.m {
		out[k] = v
	}
	return out
}
