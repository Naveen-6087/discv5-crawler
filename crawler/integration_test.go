package crawler

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	gethlog "github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/p2p/discover"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/p2p/enr"

	"github.com/Naveen-6087/discv5-crawler/nodedb"
)

// startNode runs a real go-ethereum discv5 node on a loopback socket.
func startNode(t *testing.T, extra ...enr.Entry) *discover.UDPv5 {
	t.Helper()
	key, _ := crypto.GenerateKey()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	db, _ := enode.OpenDB("")
	ln := enode.NewLocalNode(db, key)
	ln.SetStaticIP(net.IPv4(127, 0, 0, 1))
	ln.SetFallbackUDP(conn.LocalAddr().(*net.UDPAddr).Port)
	for _, e := range extra {
		ln.Set(e)
	}
	udp, err := discover.ListenV5(conn, ln, discover.Config{
		PrivateKey: key,
		// Table nodes added by hand have not passed a liveness check yet.
		NoFindnodeLivenessCheck: true,
		Log:                     gethlog.NewLogger(gethlog.DiscardHandler()),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		udp.Close()
		db.Close()
	})
	return udp
}

func TestCrawlLocalDiscv5Network(t *testing.T) {
	if testing.Short() {
		t.Skip("starts 25 UDP nodes")
	}
	const size = 25
	nodes := make([]*discover.UDPv5, size)
	for i := range nodes {
		nodes[i] = startNode(t, enr.WithEntry("cgc", uint64(i%3)))
	}
	// Each node knows its successor and two others, so the whole network is
	// reachable from node 0 only by walking the tables.
	for i, n := range nodes {
		for _, j := range []int{i + 1, i + 7, i + 13} {
			n.AddKnownNode(nodes[j%size].Self())
		}
	}

	crawlerNode := startNode(t)
	db, _ := nodedb.Open("")
	c := New(crawlerNode, db, crawlerNode.Self().ID(), Config{Workers: 6, MaxDistances: 16, Retries: 1})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := c.Run(ctx, []*enode.Node{nodes[0].Self()}); err != nil {
		t.Fatal(err)
	}
	if db.Len() != size {
		t.Fatalf("crawler found %d of %d nodes (stats: contacted %d, errors %v)",
			db.Len(), size, c.Stats().Contacted.Load(), c.Stats().Errors.Snapshot())
	}
	for _, n := range nodes {
		e, ok := db.Get(n.Self().ID())
		if !ok || !e.Responsive() {
			t.Fatalf("node %s missing or unresponsive", n.Self().ID().TerminalString())
		}
		if e.Seq != n.Self().Seq() {
			t.Errorf("stored seq %d, node is at %d", e.Seq, n.Self().Seq())
		}
	}

	// A node that changes its record is picked up by the next crawl.
	nodes[4].LocalNode().Set(enr.WithEntry("cgc", uint64(128)))
	c2 := New(crawlerNode, db, crawlerNode.Self().ID(), Config{Workers: 6, RecheckAfter: time.Nanosecond})
	if err := c2.Run(ctx, nil); err != nil {
		t.Fatal(err)
	}
	e, _ := db.Get(nodes[4].Self().ID())
	if e.Seq != nodes[4].Self().Seq() {
		t.Fatalf("updated record not stored: seq %d, want %d", e.Seq, nodes[4].Self().Seq())
	}

	// A node that went away is marked as failed.
	gone := nodes[9].Self()
	nodes[9].Close()
	c3 := New(crawlerNode, db, crawlerNode.Self().ID(), Config{Workers: 6, Retries: 0, RecheckAfter: time.Nanosecond})
	if err := c3.Run(ctx, nil); err != nil {
		t.Fatal(err)
	}
	e, _ = db.Get(gone.ID())
	if e.Responsive() || e.Failures != 1 {
		t.Fatalf("closed node: %+v", e)
	}
}
