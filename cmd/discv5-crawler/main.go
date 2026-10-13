package main

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	gethlog "github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/p2p/discover"
	"github.com/ethereum/go-ethereum/p2p/enode"

	"github.com/Naveen-6087/discv5-crawler/crawler"
	"github.com/Naveen-6087/discv5-crawler/nodedb"
	"github.com/Naveen-6087/discv5-crawler/seed"
	"github.com/Naveen-6087/discv5-crawler/stats"
)

const usage = `usage: discv5-crawler <command> [flags]

commands:
  crawl    crawl the discv5 DHT and store nodes in a JSON database
  stats    print network statistics for a database
  export   print the stored records (ENR, one per line)

run "discv5-crawler <command> -h" for flags`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "discv5-crawler:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out, logOut io.Writer) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	switch args[0] {
	case "crawl":
		return cmdCrawl(ctx, args[1:], out, logOut)
	case "stats":
		return cmdStats(args[1:], out)
	case "export":
		return cmdExport(args[1:], out)
	case "-h", "--help", "help":
		fmt.Fprintln(out, usage)
		return nil
	}
	return fmt.Errorf("unknown command %q\n%s", args[0], usage)
}

func cmdCrawl(ctx context.Context, args []string, out, logOut io.Writer) error {
	fs := flag.NewFlagSet("crawl", flag.ContinueOnError)
	dbPath := fs.String("db", "nodes.json", "node database (created if missing)")
	duration := fs.Duration("duration", 10*time.Minute, "stop after this long (0 = until the crawl completes)")
	bootFile := fs.String("bootnodes", "", "file with one ENR per line (default: mainnet discv5 bootnodes)")
	dnsLists := fs.String("dns", "", "also seed from EIP-1459 DNS lists: mainnet, sepolia, hoodi or enrtree:// URLs (comma-separated)")
	dnsMax := fs.Int("dns-max", 200, "maximum nodes taken from DNS lists")
	listen := fs.String("addr", "0.0.0.0:0", "UDP listen address")
	keyFile := fs.String("nodekey", "", "hex secp256k1 key file (default: a new key per run)")
	workers := fs.Int("workers", 256, "nodes crawled concurrently")
	depth := fs.Int("depth", 16, "maximum log-distances queried per node")
	retries := fs.Int("retries", 1, "retries per request after a timeout")
	timeout := fs.Duration("timeout", 700*time.Millisecond, "discv5 response timeout")
	recheck := fs.Duration("recheck", 30*time.Minute, "re-crawl stored nodes not tried for this long")
	flushEvery := fs.Duration("flush", 30*time.Second, "database write interval")
	progress := fs.Duration("progress", 15*time.Second, "progress log interval (0 = off)")
	verbose := fs.Bool("v", false, "debug logging")
	if err := fs.Parse(args); err != nil {
		return err
	}

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(logOut, &slog.HandlerOptions{Level: level}))

	boot, err := loadSeeds(ctx, *bootFile, *dnsLists, *dnsMax, log)
	if err != nil {
		return err
	}
	key, err := loadKey(*keyFile)
	if err != nil {
		return err
	}
	db, err := nodedb.Open(*dbPath)
	if err != nil {
		return err
	}

	addr, err := net.ResolveUDPAddr("udp", *listen)
	if err != nil {
		return err
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return err
	}
	ldb, err := enode.OpenDB("")
	if err != nil {
		conn.Close()
		return err
	}
	defer ldb.Close()
	ln := enode.NewLocalNode(ldb, key)
	// The crawler does not serve its own table; it only issues queries.
	disc, err := discover.ListenV5(conn, ln, discover.Config{
		PrivateKey:    key,
		V5RespTimeout: *timeout,
		Log:           gethlog.NewLogger(gethlog.DiscardHandler()),
	})
	if err != nil {
		conn.Close()
		return err
	}
	defer disc.Close()
	log.Info("listening", "addr", conn.LocalAddr(), "id", ln.ID().TerminalString(), "known", db.Len())

	if *duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *duration)
		defer cancel()
	}

	c := crawler.New(disc, db, ln.ID(), crawler.Config{
		Workers:      *workers,
		MaxDistances: *depth,
		Retries:      *retries,
		RecheckAfter: *recheck,
		Log:          log,
	})

	start := time.Now()
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx, boot) }()

	flushT := time.NewTicker(*flushEvery)
	defer flushT.Stop()
	var progressC <-chan time.Time
	if *progress > 0 {
		t := time.NewTicker(*progress)
		defer t.Stop()
		progressC = t.C
	}
	var runErr error
loop:
	for {
		select {
		case runErr = <-done:
			break loop
		case <-flushT.C:
			if err := db.Flush(); err != nil {
				log.Error("flush failed", "err", err)
			}
		case <-progressC:
			logProgress(log, c.Stats(), db, time.Since(start))
		}
	}
	logProgress(log, c.Stats(), db, time.Since(start))
	if err := db.Flush(); err != nil {
		return fmt.Errorf("writing %s: %w", *dbPath, err)
	}
	if runErr != nil && !errors.Is(runErr, context.DeadlineExceeded) && !errors.Is(runErr, context.Canceled) {
		return runErr
	}
	if errors.Is(runErr, context.DeadlineExceeded) || runErr == nil {
		reason := "crawl complete"
		if runErr != nil {
			reason = "duration reached"
		}
		log.Info(reason, "elapsed", time.Since(start).Round(time.Second))
	}
	return stats.Compute(db.Entries(), time.Now(), false).Print(out, 10)
}

func logProgress(log *slog.Logger, s *crawler.Stats, db *nodedb.DB, elapsed time.Duration) {
	log.Info("progress",
		"elapsed", elapsed.Round(time.Second),
		"known", db.Len(),
		"queued", s.Queued.Load(),
		"contacted", s.Contacted.Load(),
		"unreachable", s.Unreachable.Load(),
		"requests", s.Requests.Load(),
		"buckets", s.BucketQueries.Load(),
		"aborted", s.SweepAborted.Load(),
		"partial", s.Partial.Load(),
		"new", s.Discovered.Load(),
		"dup", s.Duplicates.Load(),
		"errors", fmt.Sprint(s.Errors.Snapshot()),
	)
}

func loadSeeds(ctx context.Context, bootFile, dnsLists string, dnsMax int, log *slog.Logger) ([]*enode.Node, error) {
	var nodes []*enode.Node
	var err error
	if bootFile != "" {
		nodes, err = seed.File(bootFile)
	} else {
		nodes, err = seed.MainnetBootnodes()
	}
	if err != nil {
		return nil, err
	}
	if dnsLists != "" {
		var urls []string
		for _, name := range strings.Split(dnsLists, ",") {
			u, err := seed.DNSList(strings.TrimSpace(name))
			if err != nil {
				return nil, err
			}
			urls = append(urls, u)
		}
		start := time.Now()
		fromDNS, err := seed.FromDNS(ctx, urls, seed.DNSOptions{Max: dnsMax})
		if err != nil {
			return nil, fmt.Errorf("dns seeds: %w", err)
		}
		log.Info("dns seeds", "nodes", len(fromDNS), "lists", len(urls), "elapsed", time.Since(start).Round(time.Millisecond))
		nodes = append(nodes, fromDNS...)
	}
	if len(nodes) == 0 {
		return nil, errors.New("no seed nodes")
	}
	return nodes, nil
}

func loadKey(path string) (*ecdsa.PrivateKey, error) {
	if path == "" {
		return crypto.GenerateKey()
	}
	return crypto.LoadECDSA(path)
}

func cmdStats(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("stats", flag.ContinueOnError)
	dbPath := fs.String("db", "nodes.json", "node database")
	asJSON := fs.Bool("json", false, "JSON output")
	responsive := fs.Bool("responsive", false, "only count nodes that answered at their last attempt")
	top := fs.Int("top", 15, "rows per section (0 = all)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	db, err := openExisting(*dbPath)
	if err != nil {
		return err
	}
	r := stats.Compute(db.Entries(), time.Now(), *responsive)
	if *asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	}
	return r.Print(out, *top)
}

func cmdExport(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	dbPath := fs.String("db", "nodes.json", "node database")
	responsive := fs.Bool("responsive", false, "only nodes that answered at their last attempt")
	if err := fs.Parse(args); err != nil {
		return err
	}
	db, err := openExisting(*dbPath)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(out)
	for _, e := range db.Entries() {
		if *responsive && !e.Responsive() {
			continue
		}
		fmt.Fprintln(w, e.ENR)
	}
	return w.Flush()
}

func openExisting(path string) (*nodedb.DB, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	return nodedb.Open(path)
}
