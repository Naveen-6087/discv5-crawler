package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
	gethlog "github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/p2p/discover"
	"github.com/ethereum/go-ethereum/p2p/enode"
)

func localNetwork(t *testing.T, size int) []*discover.UDPv5 {
	t.Helper()
	nodes := make([]*discover.UDPv5, size)
	for i := range nodes {
		key, _ := crypto.GenerateKey()
		conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			t.Fatal(err)
		}
		db, _ := enode.OpenDB("")
		ln := enode.NewLocalNode(db, key)
		ln.SetStaticIP(net.IPv4(127, 0, 0, 1))
		ln.SetFallbackUDP(conn.LocalAddr().(*net.UDPAddr).Port)
		nodes[i], err = discover.ListenV5(conn, ln, discover.Config{
			PrivateKey:              key,
			NoFindnodeLivenessCheck: true,
			Log:                     gethlog.NewLogger(gethlog.DiscardHandler()),
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { nodes[i].Close(); db.Close() })
	}
	for i, n := range nodes {
		n.AddKnownNode(nodes[(i+1)%size].Self())
		n.AddKnownNode(nodes[(i+5)%size].Self())
	}
	return nodes
}

func TestCrawlStatsExport(t *testing.T) {
	nodes := localNetwork(t, 12)
	dir := t.TempDir()
	boot := filepath.Join(dir, "boot.txt")
	os.WriteFile(boot, []byte("# local test network\n"+nodes[0].Self().String()+"\n"), 0o644)
	db := filepath.Join(dir, "nodes.json")

	var out, logs bytes.Buffer
	err := run(context.Background(), []string{"crawl", "-db", db, "-bootnodes", boot, "-addr", "127.0.0.1:0",
		"-duration", "30s", "-workers", "4", "-progress", "0"}, &out, &logs)
	if err != nil {
		t.Fatalf("crawl: %v\n%s", err, logs.String())
	}
	if !strings.Contains(logs.String(), "crawl complete") {
		t.Errorf("crawl did not finish on its own:\n%s", logs.String())
	}
	if !strings.Contains(out.String(), "nodes") || !strings.Contains(out.String(), "12") {
		t.Errorf("crawl report:\n%s", out.String())
	}

	out.Reset()
	if err := run(context.Background(), []string{"stats", "-db", db, "-json"}, &out, &logs); err != nil {
		t.Fatal(err)
	}
	var r struct {
		Total      int `json:"total"`
		Responsive int `json:"responsive"`
		Private    int `json:"private_or_loopback_ip"`
	}
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if r.Total != 12 || r.Responsive != 12 || r.Private != 12 {
		t.Fatalf("stats %+v", r)
	}

	out.Reset()
	if err := run(context.Background(), []string{"export", "-db", db, "-responsive"}, &out, &logs); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 12 {
		t.Fatalf("exported %d records", len(lines))
	}
	for _, l := range lines {
		if _, err := enode.Parse(enode.ValidSchemes, l); err != nil {
			t.Fatalf("exported record %q: %v", l, err)
		}
	}
}

func TestErrors(t *testing.T) {
	ctx := context.Background()
	var out, logs bytes.Buffer
	if err := run(ctx, nil, &out, &logs); err == nil {
		t.Error("no command accepted")
	}
	if err := run(ctx, []string{"bogus"}, &out, &logs); err == nil {
		t.Error("unknown command accepted")
	}
	if err := run(ctx, []string{"stats", "-db", filepath.Join(t.TempDir(), "none.json")}, &out, &logs); err == nil {
		t.Error("missing database accepted")
	}
	bad := filepath.Join(t.TempDir(), "boot.txt")
	os.WriteFile(bad, []byte("enr:not-a-record\n"), 0o644)
	if err := run(ctx, []string{"crawl", "-bootnodes", bad, "-db", ""}, &out, &logs); err == nil {
		t.Error("bad bootnode accepted")
	}
}

func TestDefaultBootnodesParse(t *testing.T) {
	nodes, err := loadBootnodes("")
	if err != nil || len(nodes) == 0 {
		t.Fatalf("mainnet bootnodes: %d %v", len(nodes), err)
	}
}
