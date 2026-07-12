package stats

import (
	"bytes"
	"encoding/binary"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/core/forkid"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/p2p/enr"

	"github.com/Naveen-6087/discv5-crawler/nodedb"
)

var now = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

func rec(t testing.TB, ip net.IP, port int, entries ...enr.Entry) *enode.Node {
	t.Helper()
	k, _ := crypto.GenerateKey()
	var r enr.Record
	r.Set(enr.IPv4(ip))
	r.Set(enr.UDP(port))
	for _, e := range entries {
		r.Set(e)
	}
	if err := enode.SignV4(&r, k); err != nil {
		t.Fatal(err)
	}
	n, _ := enode.New(enode.ValidSchemes, &r)
	return n
}

func eth2(digest [4]byte) enr.Entry {
	b := make([]byte, 16)
	copy(b, digest[:])
	binary.LittleEndian.PutUint64(b[8:], ^uint64(0))
	return enr.WithEntry("eth2", b)
}

type ethEntry struct{ ForkID forkid.ID }

func (ethEntry) ENRKey() string { return "eth" }

func fixture(t *testing.T) []nodedb.Entry {
	db, _ := nodedb.Open("")
	bpo2 := [4]byte{0x8c, 0x9f, 0x62, 0xfe}
	mainnetEL := forkid.ID{Hash: [4]byte{0x07, 0xc9, 0x46, 0x2e}}
	cl := []*enode.Node{
		rec(t, net.IP{1, 1, 1, 1}, 9000, eth2(bpo2), enr.WithEntry("cgc", uint64(4)), enr.WithEntry("attnets", []byte{0x0f, 0, 0, 0, 0, 0, 0, 0})),
		rec(t, net.IP{1, 1, 1, 1}, 9001, eth2(bpo2), enr.WithEntry("cgc", uint64(128)), enr.WithEntry("attnets", []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff})),
		rec(t, net.IP{2, 2, 2, 2}, 9000, eth2([4]byte{0xde, 0xad, 0xbe, 0xef})),
	}
	el := []*enode.Node{
		rec(t, net.IP{3, 3, 3, 3}, 30303, ethEntry{mainnetEL}),
		rec(t, net.IP{192, 168, 1, 5}, 30303, ethEntry{forkid.ID{Hash: [4]byte{1, 2, 3, 4}}}),
	}
	for _, n := range append(cl, el...) {
		db.Observe(n, now)
		db.Contacted(n, now)
	}
	failed := rec(t, net.IP{4, 4, 4, 4}, 30303)
	db.Observe(failed, now)
	db.Failed(failed.ID(), errors.New("RPC timeout"), now)
	db.Observe(rec(t, net.IP{5, 5, 5, 5}, 30303), now) // never tried
	return db.Entries()
}

func find(cs []Count, key string) (Count, bool) {
	for _, c := range cs {
		if c.Key == key {
			return c, true
		}
	}
	return Count{}, false
}

func TestCompute(t *testing.T) {
	r := Compute(fixture(t), now, false)
	if r.Total != 7 || r.Responsive != 5 || r.Failed != 1 || r.NeverTried != 1 || r.Counted != 7 {
		t.Fatalf("totals %+v", r)
	}
	if r.Consensus != 3 || r.Execution != 2 || r.DistinctIPs != 6 || r.MaxPerIP != 2 || r.Private != 1 {
		t.Errorf("composition %+v", r)
	}
	if c, _ := find(r.ForkDigests, "8c9f62fe"); c.N != 2 || c.Label != "fulu-bpo2" {
		t.Errorf("bpo2 digest %+v", c)
	}
	if c, _ := find(r.ForkDigests, "deadbeef"); c.N != 1 || c.Label != "" {
		t.Errorf("unknown digest %+v", c)
	}
	if r.ForkDigests[0].Key != "8c9f62fe" {
		t.Error("digests not sorted by count")
	}
	if c, _ := find(r.ELForks, "07c9462e/0"); c.N != 1 || c.Label != "mainnet (current)" {
		t.Errorf("mainnet EL %+v", c)
	}
	if c, _ := find(r.ELForks, "01020304/0"); c.Label != "other network" {
		t.Errorf("foreign EL %+v", c)
	}
	if c, _ := find(r.Custody, "128"); c.N != 1 {
		t.Errorf("custody %+v", r.Custody)
	}
	if r.MeanAttnets != 34 { // (4 + 64) / 2
		t.Errorf("mean attnets %v", r.MeanAttnets)
	}
	if c, _ := find(r.Failures, "timeout"); c.N != 1 {
		t.Errorf("failures %+v", r.Failures)
	}
	if c, _ := find(r.UDPPorts, "30303"); c.N != 4 {
		t.Errorf("ports %+v", r.UDPPorts)
	}

	only := Compute(fixture(t), now, true)
	if only.Counted != 5 || only.Total != 7 || len(only.UDPPorts) == 0 {
		t.Errorf("responsive-only %+v", only)
	}
	if c, _ := find(only.UDPPorts, "30303"); c.N != 2 {
		t.Errorf("responsive-only ports %+v", only.UDPPorts)
	}
}

func TestPrint(t *testing.T) {
	var buf bytes.Buffer
	if err := Compute(fixture(t), now, false).Print(&buf, 0); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"nodes", "responsive", "fork digests", "8c9f62fe", "fulu-bpo2", "mainnet (current)", "peerdas custody group count", "last error of failed nodes"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	buf.Reset()
	Compute(fixture(t), now, false).Print(&buf, 1)
	if !strings.Contains(buf.String(), "(1 more)") || strings.Contains(buf.String(), "deadbeef") {
		t.Errorf("top 1 not applied:\n%s", buf.String())
	}
	buf.Reset()
	Compute(nil, now, false).Print(&buf, 0)
	if !strings.Contains(buf.String(), "nodes") {
		t.Error("empty report")
	}
}
