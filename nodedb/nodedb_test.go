package nodedb

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/p2p/enr"
)

var t0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

// node returns key's record at the given sequence number.
func node(t *testing.T, key []byte, seq uint64, port int) *enode.Node {
	t.Helper()
	k, err := crypto.ToECDSA(key)
	if err != nil {
		t.Fatal(err)
	}
	var r enr.Record
	r.SetSeq(seq)
	r.Set(enr.IPv4(net.IP{10, 0, 0, 1}))
	r.Set(enr.UDP(port))
	if err := enode.SignV4(&r, k); err != nil {
		t.Fatal(err)
	}
	n, err := enode.New(enode.ValidSchemes, &r)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func key(b byte) []byte {
	k := make([]byte, 32)
	k[31] = b
	return k
}

func TestObserveDeduplicatesBySeq(t *testing.T) {
	db, _ := Open("")
	v1, v2, v3 := node(t, key(1), 1, 9000), node(t, key(1), 2, 9001), node(t, key(1), 3, 9002)

	if isNew, upd := db.Observe(v2, t0); !isNew || upd {
		t.Fatal("first sighting should be new")
	}
	if isNew, upd := db.Observe(v1, t0.Add(time.Second)); isNew || upd {
		t.Fatal("an older record must not replace a newer one")
	}
	if isNew, upd := db.Observe(v2, t0.Add(2*time.Second)); isNew || upd {
		t.Fatal("same record is a duplicate")
	}
	if isNew, upd := db.Observe(v3, t0.Add(3*time.Second)); isNew || !upd {
		t.Fatal("a newer record should update")
	}
	e, _ := db.Get(v1.ID())
	if db.Len() != 1 || e.Seq != 3 || e.Sightings != 4 || !e.FirstSeen.Equal(t0) || !e.LastSeen.Equal(t0.Add(3*time.Second)) {
		t.Fatalf("entry %+v", e)
	}
	if !strings.HasPrefix(e.ENR, "enr:") || e.Node().Seq() != 3 {
		t.Fatal("stored record not updated")
	}
}

func TestContactAndFailure(t *testing.T) {
	db, _ := Open("")
	n := node(t, key(2), 5, 9000)
	db.Observe(n, t0)
	db.Failed(n.ID(), errors.New("RPC timeout"), t0.Add(time.Minute))
	db.Failed(n.ID(), errors.New("RPC timeout"), t0.Add(2*time.Minute))
	e, _ := db.Get(n.ID())
	if e.Failures != 2 || e.Responsive() || e.LastError != "RPC timeout" {
		t.Fatalf("after failures %+v", e)
	}
	// The node's own answer carries a newer record.
	db.Contacted(node(t, key(2), 6, 9100), t0.Add(3*time.Minute))
	e, _ = db.Get(n.ID())
	if e.Failures != 0 || !e.Responsive() || e.Seq != 6 || e.LastError != "" {
		t.Fatalf("after contact %+v", e)
	}
	db.Failed(enode.ID{1}, errors.New("x"), t0) // unknown nodes are ignored
	if db.Len() != 1 {
		t.Fatal("failure created an entry")
	}
}

func TestDue(t *testing.T) {
	db, _ := Open("")
	fresh, stale, dead := node(t, key(3), 1, 1), node(t, key(4), 1, 1), node(t, key(5), 1, 1)
	for _, n := range []*enode.Node{fresh, stale, dead} {
		db.Observe(n, t0)
	}
	db.Contacted(fresh, t0.Add(50*time.Minute))
	db.Contacted(stale, t0)
	for i := 0; i < 5; i++ {
		db.Failed(dead.ID(), errors.New("RPC timeout"), t0)
	}
	due := db.Due(t0.Add(30*time.Minute), 5)
	if len(due) != 1 || due[0].ID() != stale.ID() {
		t.Fatalf("due %v", due)
	}
	if len(db.Due(t0.Add(30*time.Minute), 0)) != 2 {
		t.Fatal("maxFailures 0 should not filter")
	}
}

func TestPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes.json")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := byte(1); i <= 20; i++ {
		db.Observe(node(t, key(i), uint64(i), 9000+int(i)), t0)
	}
	db.Contacted(node(t, key(7), 9, 1234), t0.Add(time.Hour))
	db.Failed(node(t, key(8), 1, 1).ID(), errors.New("RPC timeout"), t0.Add(time.Hour))
	if err := db.Flush(); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	if err := db.Flush(); err != nil { // clean: no rewrite
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("clean flush changed the file")
	}
	matches, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".nodedb-*"))
	if len(matches) != 0 {
		t.Fatalf("temporary files left behind: %v", matches)
	}

	re, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	a, b := db.Entries(), re.Entries()
	if len(a) != 20 || len(b) != 20 {
		t.Fatalf("lengths %d %d", len(a), len(b))
	}
	for i := range a {
		x, y := a[i], b[i]
		x.node, y.node = nil, nil
		if x.ID != y.ID || x.ENR != y.ENR || x.Seq != y.Seq || x.Failures != y.Failures || x.LastError != y.LastError ||
			!x.LastContact.Equal(y.LastContact) || !x.LastAttempt.Equal(y.LastAttempt) || x.Sightings != y.Sightings {
			t.Fatalf("entry %d differs after reload:\n%+v\n%+v", i, x, y)
		}
		if b[i].Node() == nil {
			t.Fatal("record not parsed on load")
		}
	}
}

func TestOpenRejectsBadFiles(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte(content), 0o644)
		return p
	}
	if _, err := Open(write("garbage.json", "{not json")); err == nil {
		t.Error("garbage accepted")
	}
	if _, err := Open(write("v2.json", `{"version":2,"nodes":[]}`)); err == nil {
		t.Error("unknown version accepted")
	}
	n := node(t, key(9), 1, 1)
	mismatch := `{"version":1,"nodes":[{"id":"` + strings.Repeat("00", 32) + `","enr":"` + n.String() + `","seq":1}]}`
	if _, err := Open(write("mismatch.json", mismatch)); err == nil {
		t.Error("id/record mismatch accepted")
	}
	badENR := `{"version":1,"nodes":[{"id":"x","enr":"enr:-AAAA","seq":1}]}`
	if _, err := Open(write("badenr.json", badENR)); err == nil {
		t.Error("invalid record accepted")
	}
	if db, err := Open(filepath.Join(dir, "missing.json")); err != nil || db.Len() != 0 {
		t.Errorf("missing file: %v", err)
	}
}
