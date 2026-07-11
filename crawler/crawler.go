package crawler

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/p2p/enode"

	"github.com/Naveen-6087/discv5-crawler/nodedb"
)

// Transport is the subset of discover.UDPv5 the crawler needs.
type Transport interface {
	RequestENR(n *enode.Node) (*enode.Node, error)
	Findnode(n *enode.Node, distances []uint) ([]*enode.Node, error)
}

type Config struct {
	Workers int // concurrent nodes being crawled

	// Each node is asked for one log-distance at a time, from 256 down,
	// because a NODES reply carries at most 16 records. The sweep stops after
	// EmptyStop consecutive empty buckets or MaxDistances queries.
	MaxDistances int
	EmptyStop    int

	Retries int           // extra attempts per request after a timeout
	Backoff time.Duration // first retry delay, doubled each retry

	// Nodes already in the DB are re-crawled once they have not been tried
	// for RecheckAfter, unless they failed MaxFailures times in a row.
	RecheckAfter time.Duration
	MaxFailures  int

	Log *slog.Logger
	Now func() time.Time
}

func (c Config) withDefaults() Config {
	if c.Workers <= 0 {
		c.Workers = 16
	}
	if c.MaxDistances <= 0 {
		c.MaxDistances = 16
	}
	if c.EmptyStop <= 0 {
		c.EmptyStop = 3
	}
	if c.Retries < 0 {
		c.Retries = 0
	}
	if c.Backoff <= 0 {
		c.Backoff = 250 * time.Millisecond
	}
	if c.RecheckAfter <= 0 {
		c.RecheckAfter = 30 * time.Minute
	}
	if c.MaxFailures <= 0 {
		c.MaxFailures = 5
	}
	if c.Log == nil {
		c.Log = slog.New(slog.DiscardHandler)
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return c
}

// Crawler walks the DHT breadth-first from a set of seeds, querying every
// node it learns about exactly once per run.
type Crawler struct {
	cfg   Config
	tr    Transport
	db    *nodedb.DB
	self  enode.ID
	stats Stats

	mu      sync.Mutex
	queue   []*enode.Node
	queued  map[enode.ID]bool
	active  int
	wake    chan struct{}
	stopped bool
}

func New(tr Transport, db *nodedb.DB, self enode.ID, cfg Config) *Crawler {
	return &Crawler{
		cfg:    cfg.withDefaults(),
		tr:     tr,
		db:     db,
		self:   self,
		queued: map[enode.ID]bool{},
		wake:   make(chan struct{}, 1),
	}
}

func (c *Crawler) Stats() *Stats { return &c.stats }

// Run crawls until every reachable node has been visited or ctx is done.
// Seeds are crawled along with nodes from the DB that are due for a recheck.
func (c *Crawler) Run(ctx context.Context, seeds []*enode.Node) error {
	for _, n := range seeds {
		c.db.Observe(n, c.cfg.Now())
		c.enqueue(n)
	}
	for _, n := range c.db.Due(c.cfg.Now().Add(-c.cfg.RecheckAfter), c.cfg.MaxFailures) {
		c.enqueue(n)
	}

	var wg sync.WaitGroup
	for range c.cfg.Workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				n, ok := c.next(ctx)
				if !ok {
					return
				}
				c.visit(ctx, n)
				c.done()
			}
		}()
	}
	wg.Wait()
	return ctx.Err()
}

func (c *Crawler) enqueue(n *enode.Node) {
	if n == nil || n.ID() == c.self {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.queued[n.ID()] || c.stopped {
		return
	}
	c.queued[n.ID()] = true
	c.queue = append(c.queue, n)
	c.stats.Queued.Add(1)
	c.signal()
}

func (c *Crawler) signal() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// next blocks until there is work, returning false once the queue is empty
// with no worker active (the crawl is complete) or ctx is cancelled.
func (c *Crawler) next(ctx context.Context) (*enode.Node, bool) {
	for {
		c.mu.Lock()
		if len(c.queue) > 0 && ctx.Err() == nil {
			n := c.queue[0]
			c.queue = c.queue[1:]
			c.active++
			more := len(c.queue) > 0
			c.mu.Unlock()
			if more {
				c.signal() // hand the rest of the queue to another idle worker
			}
			return n, true
		}
		if c.active == 0 || ctx.Err() != nil {
			c.stopped = true
			c.mu.Unlock()
			c.signal() // let the other idle workers notice
			return nil, false
		}
		c.mu.Unlock()
		select {
		case <-c.wake:
		case <-ctx.Done():
		}
	}
}

func (c *Crawler) done() {
	c.mu.Lock()
	c.active--
	c.mu.Unlock()
	c.signal()
}

func (c *Crawler) visit(ctx context.Context, n *enode.Node) {
	if _, ok := n.UDPEndpoint(); !ok {
		c.stats.NoEndpoint.Add(1)
		c.db.Failed(n.ID(), errNoEndpoint, c.cfg.Now())
		return
	}
	var latest *enode.Node
	err := c.retry(ctx, func() error {
		var err error
		latest, err = c.tr.RequestENR(n)
		return err
	})
	if err != nil {
		if ctx.Err() != nil {
			return // interrupted by shutdown, not the node's fault
		}
		c.stats.Unreachable.Add(1)
		c.db.Failed(n.ID(), err, c.cfg.Now())
		c.cfg.Log.Debug("node unreachable", "id", n.ID().TerminalString(), "err", err)
		return
	}
	if latest.ID() != n.ID() {
		c.stats.Errors.record(errWrongID)
		c.db.Failed(n.ID(), errWrongID, c.cfg.Now())
		return
	}
	if latest.Seq() < n.Seq() {
		latest = n
	}
	c.db.Contacted(latest, c.cfg.Now())
	c.stats.Contacted.Add(1)

	empty := 0
	for i := 0; i < c.cfg.MaxDistances && empty < c.cfg.EmptyStop && ctx.Err() == nil; i++ {
		dist := uint(256 - i)
		var found []*enode.Node
		err := c.retry(ctx, func() error {
			var err error
			found, err = c.tr.Findnode(latest, []uint{dist})
			if err != nil && len(found) > 0 {
				// A NODES reply spans several packets and a later one was
				// lost. The records that did arrive are signed and valid.
				c.stats.Partial.Add(1)
				return nil
			}
			return err
		})
		if err != nil {
			// The node answered RequestENR; a failed bucket query is counted
			// but does not mark the node as down.
			c.stats.SweepAborted.Add(1)
			c.cfg.Log.Debug("findnode failed", "id", n.ID().TerminalString(), "dist", dist, "err", err)
			break
		}
		c.stats.BucketQueries.Add(1)
		if len(found) == 0 {
			empty++
			continue
		}
		empty = 0
		for _, f := range found {
			if f.ID() == c.self {
				continue
			}
			isNew, updated := c.db.Observe(f, c.cfg.Now())
			c.stats.Records.Add(1)
			switch {
			case isNew:
				c.stats.Discovered.Add(1)
			case updated:
				c.stats.Updated.Add(1)
			default:
				c.stats.Duplicates.Add(1)
			}
			c.enqueue(f)
		}
	}
}

// retry runs f, retrying timeouts with exponential backoff. Other errors
// (for example a failed handshake) are not retried.
func (c *Crawler) retry(ctx context.Context, f func() error) error {
	delay := c.cfg.Backoff
	for attempt := 0; ; attempt++ {
		c.stats.Requests.Add(1)
		err := f()
		if err == nil {
			return nil
		}
		class := c.stats.Errors.record(err)
		if class != ClassTimeout || attempt >= c.cfg.Retries {
			return err
		}
		c.stats.Retries.Add(1)
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return ctx.Err()
		}
		delay *= 2
	}
}
