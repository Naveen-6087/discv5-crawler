package crawler

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/p2p/enr"

	"github.com/Naveen-6087/discv5-crawler/nodedb"
)

var errRPCTimeout = errors.New("RPC timeout") // what go-ethereum returns

type fakeNode struct {
	rec      *enode.Node
	newer    *enode.Node // returned by RequestENR when set
	table    []*enode.Node
	down     bool
	flaky    int  // timeouts before the first answer
	partial  bool // FINDNODE replies lose their second half
	impostor *enode.Node
}

// fakeNet answers like a set of discv5 nodes with fixed routing tables.
type fakeNet struct {
	mu        sync.Mutex
	nodes     map[enode.ID]*fakeNode
	active    int
	maxActive int
	findnodes atomic.Int64
	delay     time.Duration
}

func (f *fakeNet) enter() {
	f.mu.Lock()
	f.active++
	f.maxActive = max(f.maxActive, f.active)
	f.mu.Unlock()
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
}

func (f *fakeNet) leave() {
	f.mu.Lock()
	f.active--
	f.mu.Unlock()
}

func (f *fakeNet) RequestENR(n *enode.Node) (*enode.Node, error) {
	f.enter()
	defer f.leave()
	f.mu.Lock()
	defer f.mu.Unlock()
	fn := f.nodes[n.ID()]
	switch {
	case fn == nil || fn.down:
		return nil, errRPCTimeout
	case fn.flaky > 0:
		fn.flaky--
		return nil, errRPCTimeout
	case fn.impostor != nil:
		return fn.impostor, nil
	case fn.newer != nil:
		return fn.newer, nil
	}
	return fn.rec, nil
}

func (f *fakeNet) Findnode(n *enode.Node, dists []uint) ([]*enode.Node, error) {
	f.enter()
	defer f.leave()
	f.findnodes.Add(1)
	f.mu.Lock()
	fn := f.nodes[n.ID()]
	f.mu.Unlock()
	if fn == nil || fn.down {
		return nil, errRPCTimeout
	}
	var out []*enode.Node
	for _, d := range dists {
		for _, t := range fn.table {
			if uint(enode.LogDist(n.ID(), t.ID())) == d && len(out) < 16 {
				out = append(out, t)
			}
		}
	}
	if fn.partial && len(out) > 1 {
		return out[:len(out)/2], errRPCTimeout
	}
	return out, nil
}

func newRecord(t testing.TB, seq uint64) (*enode.Node, func(seq uint64) *enode.Node) {
	t.Helper()
	k, _ := crypto.GenerateKey()
	mk := func(seq uint64) *enode.Node {
		var r enr.Record
		r.SetSeq(seq)
		r.Set(enr.IPv4(net.IP{10, 1, 2, 3}))
		r.Set(enr.UDP(9000))
		if err := enode.SignV4(&r, k); err != nil {
			t.Fatal(err)
		}
		n, err := enode.New(enode.ValidSchemes, &r)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	return mk(seq), mk
}

// buildNet creates n nodes whose tables form a ring plus random links, so
// every node is reachable from node 0.
func buildNet(t testing.TB, n, links int, rng *rand.Rand) (*fakeNet, []*fakeNode) {
	t.Helper()
	net := &fakeNet{nodes: map[enode.ID]*fakeNode{}}
	nodes := make([]*fakeNode, n)
	for i := range nodes {
		rec, _ := newRecord(t, 1)
		nodes[i] = &fakeNode{rec: rec}
		net.nodes[rec.ID()] = nodes[i]
	}
	for i, fn := range nodes {
		fn.table = append(fn.table, nodes[(i+1)%n].rec)
		for range links {
			fn.table = append(fn.table, nodes[rng.IntN(n)].rec)
		}
	}
	return net, nodes
}

func fastConfig() Config {
	return Config{Workers: 8, Retries: 1, Backoff: time.Millisecond, MaxDistances: 256, EmptyStop: 256}
}

func TestCrawlFindsEveryNode(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	fnet, nodes := buildNet(t, 300, 6, rng)
	down := 0
	for i := 1; i < len(nodes); i += 29 {
		nodes[i].down = true
		down++
	}
	self, _ := newRecord(t, 1)
	nodes[5].table = append(nodes[5].table, self)

	db, _ := nodedb.Open("")
	c := New(fnet, db, self.ID(), fastConfig())
	if err := c.Run(context.Background(), []*enode.Node{nodes[0].rec}); err != nil {
		t.Fatal(err)
	}
	s := c.Stats()
	if db.Len() != 300 {
		t.Fatalf("db has %d nodes, want 300", db.Len())
	}
	if _, ok := db.Get(self.ID()); ok {
		t.Fatal("crawler stored itself")
	}
	if got := s.Contacted.Load(); got != int64(300-down) {
		t.Errorf("contacted %d, want %d", got, 300-down)
	}
	if s.Unreachable.Load() != int64(down) || s.Queued.Load() != 300 {
		t.Errorf("unreachable %d queued %d", s.Unreachable.Load(), s.Queued.Load())
	}
	if s.Discovered.Load()+1 != 300 { // the seed was not discovered by a query
		t.Errorf("discovered %d", s.Discovered.Load())
	}
	if s.Records.Load() != s.Discovered.Load()+s.Updated.Load()+s.Duplicates.Load() {
		t.Error("record counters do not add up")
	}
	for _, e := range db.Entries() {
		fn := fnet.nodes[e.Node().ID()]
		if fn.down && (e.Failures != 1 || e.Responsive()) {
			t.Errorf("down node %s: %+v", e.ID[:8], e)
		}
		if !fn.down && !e.Responsive() {
			t.Errorf("live node %s not marked responsive", e.ID[:8])
		}
	}
	// Each down node was tried 1 + Retries times.
	if s.Errors.Snapshot()[ClassTimeout] != int64(2*down) {
		t.Errorf("timeouts %v", s.Errors.Snapshot())
	}
}

func TestRetries(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	fnet, nodes := buildNet(t, 40, 0, rng) // a pure ring: each node is the only path onward
	nodes[3].flaky = 1

	db, _ := nodedb.Open("")
	c := New(fnet, db, enode.ID{}, fastConfig())
	c.Run(context.Background(), []*enode.Node{nodes[0].rec})
	if db.Len() != 40 || c.Stats().Retries.Load() != 1 {
		t.Fatalf("len %d retries %d", db.Len(), c.Stats().Retries.Load())
	}

	// Without retries the flaky node fails and the ring is cut there.
	fnet2, nodes2 := buildNet(t, 40, 0, rng)
	nodes2[3].flaky = 1
	db2, _ := nodedb.Open("")
	cfg := fastConfig()
	cfg.Retries = 0
	New(fnet2, db2, enode.ID{}, cfg).Run(context.Background(), []*enode.Node{nodes2[0].rec})
	if db2.Len() != 4 {
		t.Fatalf("without retries: %d nodes, want 4", db2.Len())
	}
}

func TestPartialReplyIsKept(t *testing.T) {
	seed, _ := newRecord(t, 1)
	fnet := &fakeNet{nodes: map[enode.ID]*fakeNode{}}
	// Ten nodes in the seed's top bucket, of which only half arrive.
	var top []*enode.Node
	for len(top) < 10 {
		rec, _ := newRecord(t, 1)
		if enode.LogDist(seed.ID(), rec.ID()) == 256 {
			top = append(top, rec)
			fnet.nodes[rec.ID()] = &fakeNode{rec: rec}
		}
	}
	fnet.nodes[seed.ID()] = &fakeNode{rec: seed, table: top, partial: true}
	db, _ := nodedb.Open("")
	c := New(fnet, db, enode.ID{}, fastConfig())
	c.Run(context.Background(), []*enode.Node{seed})
	if db.Len() != 6 || c.Stats().Partial.Load() != 1 {
		t.Fatalf("got %d nodes (partial=%d), want 6", db.Len(), c.Stats().Partial.Load())
	}
}

func TestRecordUpdatesAndImpostors(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 8))
	fnet, nodes := buildNet(t, 10, 0, rng)
	_, mk := newRecord(t, 1)
	old, newer := mk(1), mk(7)
	fnet.nodes[old.ID()] = &fakeNode{rec: old, newer: newer}
	nodes[0].table = append(nodes[0].table, old)
	impostorOf, _ := newRecord(t, 1)
	other, _ := newRecord(t, 1)
	fnet.nodes[impostorOf.ID()] = &fakeNode{rec: impostorOf, impostor: other}
	nodes[0].table = append(nodes[0].table, impostorOf)

	db, _ := nodedb.Open("")
	c := New(fnet, db, enode.ID{}, fastConfig())
	c.Run(context.Background(), []*enode.Node{nodes[0].rec})
	e, _ := db.Get(old.ID())
	if e.Seq != 7 || !e.Responsive() {
		t.Errorf("newer record not stored: %+v", e)
	}
	e, _ = db.Get(impostorOf.ID())
	if e.Responsive() || e.LastError != errWrongID.Error() {
		t.Errorf("impostor accepted: %+v", e)
	}
	if _, ok := db.Get(other.ID()); ok {
		t.Error("impostor's record stored")
	}
}

func TestWorkerLimit(t *testing.T) {
	rng := rand.New(rand.NewPCG(9, 10))
	fnet, nodes := buildNet(t, 120, 4, rng)
	fnet.delay = 200 * time.Microsecond
	db, _ := nodedb.Open("")
	cfg := fastConfig()
	cfg.Workers = 5
	New(fnet, db, enode.ID{}, cfg).Run(context.Background(), []*enode.Node{nodes[0].rec})
	if db.Len() != 120 {
		t.Fatalf("found %d", db.Len())
	}
	if fnet.maxActive > 5 || fnet.maxActive < 2 {
		t.Fatalf("max concurrent requests %d with 5 workers", fnet.maxActive)
	}
}

func TestSweepStopsAtEmptyBuckets(t *testing.T) {
	rng := rand.New(rand.NewPCG(11, 12))
	fnet, nodes := buildNet(t, 50, 3, rng)
	db, _ := nodedb.Open("")
	cfg := fastConfig()
	cfg.MaxDistances, cfg.EmptyStop = 16, 2
	New(fnet, db, enode.ID{}, cfg).Run(context.Background(), []*enode.Node{nodes[0].rec})
	// With ~4 entries per table nearly all lie at distances 256..253, so a
	// sweep needs far fewer than 16 queries per node.
	if q := fnet.findnodes.Load(); q > 50*8 {
		t.Fatalf("%d FINDNODE queries for 50 nodes", q)
	}
}

func TestCancellation(t *testing.T) {
	rng := rand.New(rand.NewPCG(13, 14))
	fnet, nodes := buildNet(t, 400, 4, rng)
	fnet.delay = 5 * time.Millisecond
	db, _ := nodedb.Open("")
	c := New(fnet, db, enode.ID{}, fastConfig())
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := c.Run(ctx, []*enode.Node{nodes[0].rec})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err %v", err)
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("took %v to stop", el)
	}
	if c.Stats().Unreachable.Load() != 0 {
		t.Fatal("cancelled requests counted as unreachable nodes")
	}
}

func TestResumeFromDB(t *testing.T) {
	rng := rand.New(rand.NewPCG(15, 16))
	fnet, nodes := buildNet(t, 60, 3, rng)
	db, _ := nodedb.Open("")
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	cfg := fastConfig()
	cfg.Now = func() time.Time { return now }
	New(fnet, db, enode.ID{}, cfg).Run(context.Background(), []*enode.Node{nodes[0].rec})

	// Ten minutes later nothing is due yet.
	now = now.Add(10 * time.Minute)
	cfg.RecheckAfter = time.Hour
	c := New(fnet, db, enode.ID{}, cfg)
	before := fnet.findnodes.Load()
	c.Run(context.Background(), nil)
	if fnet.findnodes.Load() != before || c.Stats().Contacted.Load() != 0 {
		t.Fatal("nodes re-crawled before they were due")
	}

	// Two hours later every stored node is due again, without any seed.
	now = now.Add(2 * time.Hour)
	c = New(fnet, db, enode.ID{}, cfg)
	c.Run(context.Background(), nil)
	if c.Stats().Contacted.Load() != 60 {
		t.Fatalf("re-crawl contacted %d", c.Stats().Contacted.Load())
	}
}

func TestClassify(t *testing.T) {
	cases := map[string]string{
		"RPC timeout":                           ClassTimeout,
		"handshake timeout":                     ClassTimeout,
		"no WHOAREYOU challenge":                ClassHandshake,
		"use of closed network connection":      ClassClosed,
		"0 nodes in response for distance zero": ClassInvalid,
		"something else":                        ClassOther,
	}
	for msg, want := range cases {
		if got := Classify(errors.New(msg)); got != want {
			t.Errorf("%q: %s, want %s", msg, got, want)
		}
	}
	if Classify(fmt.Errorf("wrapped: %w", context.DeadlineExceeded)) != ClassTimeout {
		t.Error("deadline")
	}
}
