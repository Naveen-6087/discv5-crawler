package crawler

import (
	"context"
	"math/rand/v2"
	"testing"

	"github.com/ethereum/go-ethereum/p2p/enode"

	"github.com/Naveen-6087/discv5-crawler/nodedb"
)

// BenchmarkCrawl measures the crawler's own overhead (queueing, dedup, DB
// updates) on a 2,000-node in-memory network with no network latency.
func BenchmarkCrawl(b *testing.B) {
	fnet, nodes := buildNet(b, 2000, 12, rand.New(rand.NewPCG(1, 1)))
	for b.Loop() {
		db, _ := nodedb.Open("")
		c := New(fnet, db, enode.ID{}, Config{Workers: 32, MaxDistances: 16})
		if err := c.Run(context.Background(), []*enode.Node{nodes[0].rec}); err != nil {
			b.Fatal(err)
		}
		if db.Len() != 2000 {
			b.Fatalf("found %d", db.Len())
		}
	}
}
