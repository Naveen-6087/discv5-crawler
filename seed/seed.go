package seed

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	gethlog "github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/p2p/dnsdisc"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/params"
)

// MainnetBootnodes are the consensus-layer discv5 bootnodes shipped with go-ethereum.
func MainnetBootnodes() ([]*enode.Node, error) {
	return parseAll(params.V5Bootnodes)
}

// File reads one ENR or enode URL per line; blank lines and # comments are skipped.
func File(path string) ([]*enode.Node, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if l != "" && !strings.HasPrefix(l, "#") {
			lines = append(lines, l)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return parseAll(lines)
}

func parseAll(lines []string) ([]*enode.Node, error) {
	var nodes []*enode.Node
	for _, l := range lines {
		n, err := enode.Parse(enode.ValidSchemes, l)
		if err != nil {
			return nil, fmt.Errorf("bootnode %q: %w", l, err)
		}
		nodes = append(nodes, n)
	}
	return nodes, nil
}

// DNSList expands a network name (mainnet, sepolia, hoodi) to the
// Ethereum Foundation's EIP-1459 node list for it; any other value must
// already be an enrtree:// URL.
func DNSList(name string) (string, error) {
	if strings.HasPrefix(name, "enrtree://") {
		return name, nil
	}
	switch name {
	case "mainnet":
		return params.KnownDNSNetwork(params.MainnetGenesisHash, "all"), nil
	case "sepolia":
		return params.KnownDNSNetwork(params.SepoliaGenesisHash, "all"), nil
	case "hoodi":
		return params.KnownDNSNetwork(params.HoodiGenesisHash, "all"), nil
	}
	return "", fmt.Errorf("unknown DNS list %q: use mainnet, sepolia, hoodi or an enrtree:// URL", name)
}

// DNSOptions bounds a DNS seed walk.
type DNSOptions struct {
	Max       int           // stop after this many distinct nodes (default 200)
	Timeout   time.Duration // overall deadline (default 30s)
	RateLimit float64       // DNS queries per second (default 20)
	Resolver  dnsdisc.Resolver
}

// FromDNS walks EIP-1459 trees at random and returns up to opts.Max distinct
// nodes. Every record is verified against the tree's signed root. Running
// into the deadline is not an error if some nodes were found.
func FromDNS(ctx context.Context, urls []string, opts DNSOptions) ([]*enode.Node, error) {
	if opts.Max <= 0 {
		opts.Max = 200
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 30 * time.Second
	}
	if opts.RateLimit <= 0 {
		opts.RateLimit = 20
	}
	client := dnsdisc.NewClient(dnsdisc.Config{
		RateLimit: opts.RateLimit,
		Resolver:  opts.Resolver,
		Logger:    gethlog.NewLogger(gethlog.DiscardHandler()),
	})
	it, err := client.NewIterator(urls...)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	go func() {
		<-ctx.Done()
		it.Close()
	}()
	seen := map[enode.ID]bool{}
	var nodes []*enode.Node
	for len(nodes) < opts.Max && it.Next() {
		n := it.Node()
		if !seen[n.ID()] {
			seen[n.ID()] = true
			nodes = append(nodes, n)
		}
	}
	it.Close()
	if len(nodes) == 0 {
		if ctx.Err() != nil {
			return nil, errors.New("no nodes resolved from DNS before the deadline")
		}
		return nil, errors.New("DNS lists returned no nodes")
	}
	return nodes, nil
}
