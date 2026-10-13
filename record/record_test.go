package record

import (
	"encoding/binary"
	"encoding/hex"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/core/forkid"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/p2p/enr"
	"github.com/ethereum/go-ethereum/rlp"
)

// The example record from EIP-778.
const eip778Example = "enr:-IS4QHCYrYZbAKWCBRlAy5zzaDZXJBGkcnh4MHcBFZntXNFrdvJjX04jRzjzCBOonrkTfj499SZuOh8R33Ls8RRcy5wBgmlkgnY0gmlwhH8AAAGJc2VjcDI1NmsxoQPKY0yuDUmstAHYpMa2_oxVtw0RW_QAdpzBQA8yWM0xOIN1ZHCCdl8"

// A mainnet discv5 bootnode record (Teku team) as shipped in go-ethereum.
const tekuBootnode = "enr:-KG4QMOEswP62yzDjSwWS4YEjtTZ5PO6r65CPqYBkgTTkrpaedQ8uEUo1uMALtJIvb2w_WWEVmg5yt1UAuK1ftxUU7QDhGV0aDKQu6TalgMAAAD__________4JpZIJ2NIJpcIQEnfA2iXNlY3AyNTZrMaEDfol8oLr6XJ7FsdAYE7lpJhKMls4G_v6qQOGKJUWGb_uDdGNwgiMog3VkcIIjKA"

func TestEIP778Example(t *testing.T) {
	n := enode.MustParse(eip778Example)
	info, err := Parse(n)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.ID.String(); got != "a448f24c6d18e575453db13171562b71999873db5b286df957af199ec94617f7" {
		t.Errorf("id %s", got)
	}
	if info.Seq != 1 || info.IP4 != netip.MustParseAddr("127.0.0.1") || info.UDP != 30303 || info.TCP != 0 {
		t.Errorf("decoded %+v", info)
	}
	if want := []string{"id", "ip", "secp256k1", "udp"}; !equal(info.Keys, want) {
		t.Errorf("keys %v, want %v", info.Keys, want)
	}
	if info.Eth2 != nil || info.Eth != nil || info.CGC != -1 || info.Attnets != -1 {
		t.Error("absent entries decoded")
	}
}

func TestBootnodeEth2(t *testing.T) {
	info, err := Parse(enode.MustParse(tekuBootnode))
	if err != nil {
		t.Fatal(err)
	}
	if info.Eth2 == nil {
		t.Fatal("no eth2 entry")
	}
	if got := hex.EncodeToString(info.Eth2.ForkDigest[:]); got != "bba4da96" || DigestLabel(info.Eth2.ForkDigest) != "capella" {
		t.Errorf("digest %s (%q)", got, DigestLabel(info.Eth2.ForkDigest))
	}
	if info.Eth2.NextForkVersion != [4]byte{3, 0, 0, 0} || info.Eth2.NextForkEpoch != ^uint64(0) {
		t.Errorf("next fork %x@%d", info.Eth2.NextForkVersion, info.Eth2.NextForkEpoch)
	}
	if info.IP4 != netip.MustParseAddr("4.157.240.54") || info.UDP != 9000 || info.TCP != 9000 {
		t.Errorf("endpoint %v udp %d tcp %d", info.IP4, info.UDP, info.TCP)
	}
}

// Mainnet fork digests as gossiped on the network. The pre-Fulu values are
// the well-known ones; fulu-bpo2 was the digest of 70% of consensus nodes in
// a crawl on 2026-10-11.
func TestMainnetDigests(t *testing.T) {
	want := map[string]string{
		"b5303f2a": "phase0",
		"afcaaba0": "altair",
		"4a26c58b": "bellatrix",
		"bba4da96": "capella",
		"6a95a1a9": "deneb",
		"ad532ceb": "electra",
		"8c9f62fe": "fulu-bpo2",
	}
	for h, name := range want {
		var d [4]byte
		hex.Decode(d[:], []byte(h))
		if got := DigestLabel(d); got != name {
			t.Errorf("%s: got %q, want %q", h, got, name)
		}
	}
	labels := map[string]bool{}
	for _, l := range digestLabels {
		labels[l] = true
	}
	for _, l := range []string{"fulu", "fulu-bpo1"} {
		if !labels[l] {
			t.Errorf("no digest for %s", l)
		}
	}
	if DigestLabel([4]byte{1, 2, 3, 4}) != "" {
		t.Error("unknown digest labelled")
	}
}

func signed(t *testing.T, entries ...enr.Entry) *enode.Node {
	t.Helper()
	key, _ := crypto.GenerateKey()
	var r enr.Record
	r.Set(enr.IPv4(net.IP{1, 2, 3, 4}))
	r.Set(enr.UDP(9000))
	for _, e := range entries {
		r.Set(e)
	}
	if err := enode.SignV4(&r, key); err != nil {
		t.Fatal(err)
	}
	n, err := enode.New(enode.ValidSchemes, &r)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestConsensusEntries(t *testing.T) {
	eth2 := make([]byte, 16)
	copy(eth2, []byte{0x8c, 0x9f, 0x62, 0xfe, 6, 0, 0, 0})
	binary.LittleEndian.PutUint64(eth2[8:], 500000)
	n := signed(t,
		enr.WithEntry("eth2", eth2),
		enr.WithEntry("attnets", []byte{0xff, 0x01, 0, 0, 0, 0, 0, 0x80}),
		enr.WithEntry("syncnets", []byte{0x05}),
		enr.WithEntry("cgc", uint64(128)),
		enr.WithEntry("nfd", []byte{1, 2, 3, 4}),
		enr.QUIC(9001),
	)
	info, err := Parse(n)
	if err != nil {
		t.Fatal(err)
	}
	if info.Eth2.NextForkEpoch != 500000 || info.Eth2.NextForkVersion != [4]byte{6, 0, 0, 0} {
		t.Errorf("eth2 %+v", info.Eth2)
	}
	if info.Attnets != 10 || info.Syncnets != 2 || info.CGC != 128 || info.QUIC != 9001 {
		t.Errorf("attnets %d syncnets %d cgc %d quic %d", info.Attnets, info.Syncnets, info.CGC, info.QUIC)
	}
	if info.NFD == nil || *info.NFD != [4]byte{1, 2, 3, 4} {
		t.Errorf("nfd %v", info.NFD)
	}
}

type ethList struct {
	ForkID forkid.ID
}

func (ethList) ENRKey() string { return "eth" }

func TestExecutionEntry(t *testing.T) {
	id := forkid.ID{Hash: [4]byte{0x07, 0xc9, 0x46, 0x2e}}
	info, err := Parse(signed(t, ethList{id}))
	if err != nil || len(info.Eth) != 1 || info.Eth[0] != id || len(info.Quirks) != 0 {
		t.Fatalf("standard entry: %+v %v", info.Eth, err)
	}

	// The OP Stack encoding wraps the list in a byte string.
	inner, _ := rlp.EncodeToBytes(ethList{id})
	info, err = Parse(signed(t, enr.WithEntry("eth", inner)))
	if err != nil || len(info.Eth) != 1 || info.Eth[0] != id {
		t.Fatalf("wrapped entry: %+v %v", info.Eth, err)
	}
	if len(info.Quirks) != 1 || info.Quirks[0] != QuirkWrappedEth {
		t.Errorf("quirks %v", info.Quirks)
	}

	info, err = Parse(signed(t, enr.WithEntry("eth", []uint{1, 2})))
	if err == nil || info.Eth != nil {
		t.Errorf("malformed entry accepted: %+v", info.Eth)
	}
	if info.UDP != 9000 {
		t.Error("a malformed entry should not hide the rest of the record")
	}
}

func TestByteListEncoding(t *testing.T) {
	// Seen on mainnet: eth2 as a list of sixteen one-byte integers.
	list := []uint{0x8c, 0x9f, 0x62, 0xfe, 6, 0, 0, 0, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
	info, err := Parse(signed(t, enr.WithEntry("eth2", list), enr.WithEntry("attnets", []uint{0xff, 0, 0, 0, 0, 0, 0, 1})))
	if err != nil {
		t.Fatal(err)
	}
	if info.Eth2 == nil || DigestLabel(info.Eth2.ForkDigest) != "fulu-bpo2" || info.Eth2.NextForkEpoch != ^uint64(0) {
		t.Fatalf("eth2 %+v", info.Eth2)
	}
	if info.Attnets != 9 || len(info.Quirks) != 2 || info.Quirks[0] != "eth2"+QuirkByteList {
		t.Errorf("attnets %d quirks %v", info.Attnets, info.Quirks)
	}
	if _, err := Parse(signed(t, enr.WithEntry("eth2", []uint{1, 256}))); err == nil {
		t.Error("list element above 255 accepted")
	}
}

func TestClientEntry(t *testing.T) {
	info, err := Parse(signed(t, enr.WithEntry("client", []string{"Lighthouse", "8.2.3", "x86_64-linux"})))
	if err != nil || info.Client == nil || *info.Client != (Client{"Lighthouse", "8.2.3"}) {
		t.Fatalf("client %+v %v", info.Client, err)
	}
	info, err = Parse(signed(t, enr.WithEntry("client", []string{"Grandine"})))
	if err != nil || info.Client == nil || info.Client.Name != "Grandine" || info.Client.Version != "" {
		t.Fatalf("name only: %+v %v", info.Client, err)
	}
	if info, err := Parse(signed(t, enr.WithEntry("client", uint64(7)))); err == nil || info.Client != nil {
		t.Fatalf("malformed client accepted: %+v", info.Client)
	}
	if info, _ := Parse(signed(t)); info.Client != nil {
		t.Fatal("absent client decoded")
	}
}

func TestShortEth2IsReported(t *testing.T) {
	info, err := Parse(signed(t, enr.WithEntry("eth2", []byte{1, 2, 3})))
	if err == nil || info.Eth2 != nil {
		t.Fatalf("short eth2 accepted: %+v", info.Eth2)
	}
}

func TestClassifyEL(t *testing.T) {
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		id   forkid.ID
		want ELForkClass
	}{
		{forkid.ID{Hash: [4]byte{0x07, 0xc9, 0x46, 0x2e}}, ELCurrent},
		{forkid.ID{Hash: [4]byte{0xfc, 0x64, 0xec, 0x04}, Next: 1150000}, ELCompatible}, // Frontier
		{forkid.ID{Hash: [4]byte{0xde, 0xad, 0xbe, 0xef}}, ELIncompatible},
	}
	for _, c := range cases {
		if got := ClassifyEL(c.id, now); got != c.want {
			t.Errorf("%x/%d: %s, want %s", c.id.Hash, c.id.Next, got, c.want)
		}
	}
}

func FuzzParse(f *testing.F) {
	f.Add([]byte{0x8c, 0x9f, 0x62, 0xfe}, []byte{0xc7, 0xc6, 0x84, 1, 2, 3, 4, 0x80}, uint64(4))
	f.Add([]byte{}, []byte{0x88, 0xc7, 0xc6, 0x84, 1, 2, 3, 4, 0x80}, uint64(0))
	f.Fuzz(func(t *testing.T, eth2, eth []byte, cgc uint64) {
		if len(eth2) > 64 || len(eth) > 64 {
			t.Skip("records are limited to 300 bytes")
		}
		var r enr.Record
		r.Set(enr.WithEntry("eth2", eth2))
		r.Set(enr.WithEntry("attnets", eth2))
		r.Set(enr.WithEntry("cgc", cgc))
		if _, _, rest, err := rlp.Split(eth); err == nil && len(rest) == 0 {
			r.Set(enr.WithEntry("eth", rlp.RawValue(eth)))
		}
		var id enode.ID
		n := enode.SignNull(&r, id)
		info, _ := Parse(n)
		if info.Eth2 != nil && len(eth2) < 16 {
			t.Fatal("eth2 decoded from a short value")
		}
	})
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func BenchmarkParse(b *testing.B) {
	n := enode.MustParse(tekuBootnode)
	for b.Loop() {
		if _, err := Parse(n); err != nil {
			b.Fatal(err)
		}
	}
}
