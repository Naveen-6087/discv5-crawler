package seed

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/p2p/dnsdisc"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/p2p/enr"
)

type mapResolver map[string]string

func (m mapResolver) LookupTXT(_ context.Context, name string) ([]string, error) {
	if txt, ok := m[name]; ok {
		return []string{txt}, nil
	}
	return nil, fmt.Errorf("no TXT record for %s", name)
}

func testNodes(t *testing.T, n int) []*enode.Node {
	t.Helper()
	var out []*enode.Node
	for i := range n {
		key, _ := crypto.GenerateKey()
		var r enr.Record
		r.Set(enr.IPv4(net.IP{10, 0, byte(i >> 8), byte(i)}))
		r.Set(enr.UDP(30303))
		if err := enode.SignV4(&r, key); err != nil {
			t.Fatal(err)
		}
		node, _ := enode.New(enode.ValidSchemes, &r)
		out = append(out, node)
	}
	return out
}

func signedTree(t *testing.T, nodes []*enode.Node) (string, mapResolver) {
	t.Helper()
	tree, err := dnsdisc.MakeTree(1, nodes, nil)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := crypto.GenerateKey()
	url, err := tree.Sign(key, "nodes.example.org")
	if err != nil {
		t.Fatal(err)
	}
	return url, mapResolver(tree.ToTXT("nodes.example.org"))
}

func TestFromDNS(t *testing.T) {
	nodes := testNodes(t, 40)
	url, res := signedTree(t, nodes)
	got, err := FromDNS(context.Background(), []string{url}, DNSOptions{Max: 40, Timeout: 20 * time.Second, Resolver: res, RateLimit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	want := map[enode.ID]bool{}
	for _, n := range nodes {
		want[n.ID()] = true
	}
	if len(got) != 40 {
		t.Fatalf("resolved %d of 40 nodes", len(got))
	}
	for _, n := range got {
		if !want[n.ID()] {
			t.Fatalf("node %s is not in the tree", n.ID())
		}
	}

	few, err := FromDNS(context.Background(), []string{url}, DNSOptions{Max: 5, Resolver: res, RateLimit: 1000})
	if err != nil || len(few) != 5 {
		t.Fatalf("max 5: %d %v", len(few), err)
	}
}

func TestFromDNSRejectsBadSignature(t *testing.T) {
	nodes := testNodes(t, 5)
	url, res := signedTree(t, nodes)
	// The same tree, signed by a different key than the URL names.
	other, _ := crypto.GenerateKey()
	tree, _ := dnsdisc.MakeTree(1, nodes, nil)
	tree.Sign(other, "nodes.example.org")
	for k, v := range tree.ToTXT("nodes.example.org") {
		res[k] = v
	}
	if _, err := FromDNS(context.Background(), []string{url}, DNSOptions{Timeout: 2 * time.Second, Resolver: res, RateLimit: 1000}); err == nil {
		t.Fatal("tree with a foreign signature accepted")
	}
}

func TestDNSList(t *testing.T) {
	u, err := DNSList("mainnet")
	if err != nil || !strings.HasSuffix(u, "@all.mainnet.ethdisco.net") {
		t.Fatalf("mainnet: %s %v", u, err)
	}
	if u, _ := DNSList("enrtree://AAAA@x.org"); u != "enrtree://AAAA@x.org" {
		t.Fatal("URL not passed through")
	}
	if _, err := DNSList("goerli"); err == nil {
		t.Fatal("unknown network accepted")
	}
}

func TestFileAndBootnodes(t *testing.T) {
	boot, err := MainnetBootnodes()
	if err != nil || len(boot) == 0 {
		t.Fatalf("bootnodes: %d %v", len(boot), err)
	}
	p := filepath.Join(t.TempDir(), "seeds.txt")
	os.WriteFile(p, []byte("# comment\n\n"+boot[0].String()+"\n"), 0o644)
	got, err := File(p)
	if err != nil || len(got) != 1 || got[0].ID() != boot[0].ID() {
		t.Fatalf("file: %v %v", got, err)
	}
	os.WriteFile(p, []byte("enr:-broken\n"), 0o644)
	if _, err := File(p); err == nil {
		t.Fatal("broken record accepted")
	}
}
