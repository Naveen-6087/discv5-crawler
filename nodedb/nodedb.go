package nodedb

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/p2p/enode"
)

const fileVersion = 1

// Entry is what the crawler knows about one node, keyed by node ID. Only the
// record with the highest sequence number is kept.
type Entry struct {
	ID          string    `json:"id"`
	ENR         string    `json:"enr"`
	Seq         uint64    `json:"seq"`
	FirstSeen   time.Time `json:"first_seen"`
	LastSeen    time.Time `json:"last_seen"`
	LastContact time.Time `json:"last_contact,omitzero"`
	LastAttempt time.Time `json:"last_attempt,omitzero"`
	Failures    int       `json:"failures,omitempty"` // consecutive failed contacts
	LastError   string    `json:"last_error,omitempty"`
	Sightings   int       `json:"sightings"` // times returned in NODES responses

	node *enode.Node
}

// Node returns the parsed record.
func (e *Entry) Node() *enode.Node { return e.node }

// Responsive reports whether the node answered at its most recent attempt.
func (e *Entry) Responsive() bool {
	return !e.LastContact.IsZero() && !e.LastContact.Before(e.LastAttempt)
}

type file struct {
	Version int      `json:"version"`
	Nodes   []*Entry `json:"nodes"`
}

// DB is an in-memory node table persisted as a single JSON file. Writes go
// to a temporary file that is renamed over the old one, so a crash leaves a
// complete file behind.
type DB struct {
	mu    sync.Mutex
	path  string
	nodes map[enode.ID]*Entry
	dirty bool
}

// Open loads path if it exists; an empty path gives a memory-only DB.
func Open(path string) (*DB, error) {
	db := &DB{path: path, nodes: map[enode.ID]*Entry{}}
	if path == "" {
		return db, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return db, nil
	}
	if err != nil {
		return nil, err
	}
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("nodedb: %s: %w", path, err)
	}
	if f.Version != fileVersion {
		return nil, fmt.Errorf("nodedb: %s: unsupported version %d", path, f.Version)
	}
	for _, e := range f.Nodes {
		n, err := enode.Parse(enode.ValidSchemes, e.ENR)
		if err != nil {
			return nil, fmt.Errorf("nodedb: %s: node %s: %w", path, e.ID, err)
		}
		if n.ID().String() != e.ID || n.Seq() != e.Seq {
			return nil, fmt.Errorf("nodedb: %s: node %s does not match its record", path, e.ID)
		}
		e.node = n
		db.nodes[n.ID()] = e
	}
	return db, nil
}

// Observe records that n was returned by another node. It reports whether
// the node is new and whether its record replaced an older one.
func (db *DB) Observe(n *enode.Node, now time.Time) (isNew, updated bool) {
	db.mu.Lock()
	defer db.mu.Unlock()
	e, ok := db.nodes[n.ID()]
	if !ok {
		e = &Entry{ID: n.ID().String(), FirstSeen: now}
		db.nodes[n.ID()] = e
		e.setRecord(n)
		isNew = true
	} else if n.Seq() > e.Seq {
		e.setRecord(n)
		updated = true
	}
	e.LastSeen = now
	e.Sightings++
	db.dirty = true
	return isNew, updated
}

// Contacted records a successful direct exchange, with the node's own record
// (which may be newer than what other nodes returned).
func (db *DB) Contacted(n *enode.Node, now time.Time) {
	db.mu.Lock()
	defer db.mu.Unlock()
	e, ok := db.nodes[n.ID()]
	if !ok {
		e = &Entry{ID: n.ID().String(), FirstSeen: now}
		db.nodes[n.ID()] = e
	}
	if e.node == nil || n.Seq() >= e.Seq {
		e.setRecord(n)
	}
	e.LastSeen, e.LastContact, e.LastAttempt = now, now, now
	e.Failures, e.LastError = 0, ""
	db.dirty = true
}

// Failed records a contact attempt that did not get an answer.
func (db *DB) Failed(id enode.ID, cause error, now time.Time) {
	db.mu.Lock()
	defer db.mu.Unlock()
	e, ok := db.nodes[id]
	if !ok {
		return
	}
	e.LastAttempt = now
	e.Failures++
	e.LastError = cause.Error()
	db.dirty = true
}

func (e *Entry) setRecord(n *enode.Node) {
	e.node = n
	e.ENR = n.String()
	e.Seq = n.Seq()
}

// Get returns a copy of the entry for id.
func (db *DB) Get(id enode.ID) (Entry, bool) {
	db.mu.Lock()
	defer db.mu.Unlock()
	e, ok := db.nodes[id]
	if !ok {
		return Entry{}, false
	}
	return *e, true
}

func (db *DB) Len() int {
	db.mu.Lock()
	defer db.mu.Unlock()
	return len(db.nodes)
}

// Entries returns copies of all entries ordered by node ID.
func (db *DB) Entries() []Entry {
	db.mu.Lock()
	defer db.mu.Unlock()
	out := make([]Entry, 0, len(db.nodes))
	for _, e := range db.nodes {
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Due returns nodes not attempted since before, skipping nodes that have
// failed maxFailures times in a row.
func (db *DB) Due(before time.Time, maxFailures int) []*enode.Node {
	var out []*enode.Node
	for _, e := range db.Entries() {
		if e.LastAttempt.Before(before) && (maxFailures <= 0 || e.Failures < maxFailures) {
			out = append(out, e.node)
		}
	}
	return out
}

// Flush writes the DB to disk if it changed since the last flush.
func (db *DB) Flush() error {
	if db.path == "" {
		return nil
	}
	db.mu.Lock()
	if !db.dirty {
		db.mu.Unlock()
		return nil
	}
	f := file{Version: fileVersion}
	for _, e := range db.nodes {
		c := *e
		f.Nodes = append(f.Nodes, &c)
	}
	db.dirty = false
	db.mu.Unlock()

	sort.Slice(f.Nodes, func(i, j int) bool { return f.Nodes[i].ID < f.Nodes[j].ID })
	data, err := json.MarshalIndent(f, "", " ")
	if err != nil {
		return err
	}
	if err := writeAtomic(db.path, data); err != nil {
		db.mu.Lock()
		db.dirty = true
		db.mu.Unlock()
		return err
	}
	return nil
}

func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".nodedb-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}
