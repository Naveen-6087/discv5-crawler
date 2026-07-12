package stats

import (
	"net"
	"testing"

	"github.com/ethereum/go-ethereum/p2p/enr"

	"github.com/Naveen-6087/discv5-crawler/nodedb"
)

func BenchmarkCompute10k(b *testing.B) {
	db, _ := nodedb.Open("")
	digests := [][4]byte{{0x8c, 0x9f, 0x62, 0xfe}, {0xde, 0xad, 0xbe, 0xef}, {0xad, 0x53, 0x2c, 0xeb}}
	for i := range 10000 {
		ip := net.IP{byte(i >> 16), byte(i >> 8), byte(i), 1}
		n := rec(b, ip, 9000+i%7, eth2(digests[i%3]), enr.WithEntry("cgc", uint64(i%129)))
		db.Observe(n, now)
		if i%2 == 0 {
			db.Contacted(n, now)
		}
	}
	entries := db.Entries()
	for b.Loop() {
		Compute(entries, now, false)
	}
}
