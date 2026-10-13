package stats

import (
	"encoding/hex"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Naveen-6087/discv5-crawler/nodedb"
	"github.com/Naveen-6087/discv5-crawler/record"
)

type Count struct {
	Key   string `json:"key"`
	Label string `json:"label,omitempty"`
	N     int    `json:"n"`
}

// Report summarises a node DB.
type Report struct {
	Generated time.Time `json:"generated"`

	Total       int `json:"total"`
	Responsive  int `json:"responsive"`  // answered at the last attempt
	Failed      int `json:"failed"`      // last attempt failed
	NeverTried  int `json:"never_tried"` // seen but not yet contacted
	Counted     int `json:"counted"`     // nodes included in the sections below
	Malformed   int `json:"malformed"`   // records with undecodable Ethereum entries
	DistinctIPs int `json:"distinct_ips"`
	MaxPerIP    int `json:"max_nodes_per_ip"`
	IPv4        int `json:"ipv4"`
	IPv6        int `json:"ipv6"`
	Private     int `json:"private_or_loopback_ip"`

	Consensus   int     `json:"consensus"` // records with an eth2 entry
	Execution   int     `json:"execution"` // records with an eth entry
	ForkDigests []Count `json:"fork_digests"`
	NextForks   []Count `json:"next_fork_versions"`
	ELForks     []Count `json:"execution_fork_ids"`
	Custody     []Count `json:"custody_group_counts"`
	MeanAttnets float64 `json:"mean_attnets"`
	Keys        []Count `json:"enr_keys"`
	Quirks      []Count `json:"quirks"`
	Clients     []Count `json:"clients"`
	Versions    []Count `json:"client_versions"`
	UDPPorts    []Count `json:"udp_ports"`
	Failures    []Count `json:"last_errors"`
}

// Compute builds a report. Only responsive nodes are counted in the network
// composition sections when onlyResponsive is set.
func Compute(entries []nodedb.Entry, now time.Time, onlyResponsive bool) Report {
	r := Report{Generated: now.UTC()}
	digests := map[string]int{}
	next := map[string]int{}
	el := map[string]int{}
	elLabel := map[string]string{}
	custody := map[string]int{}
	keys := map[string]int{}
	quirks := map[string]int{}
	clients := map[string]int{}
	versions := map[string]int{}
	ports := map[string]int{}
	fails := map[string]int{}
	ips := map[string]int{}
	attSum, attN := 0, 0

	for i := range entries {
		e := &entries[i]
		r.Total++
		switch {
		case e.LastAttempt.IsZero():
			r.NeverTried++
		case e.Responsive():
			r.Responsive++
		default:
			r.Failed++
			fails[failureClass(e.LastError)]++
		}
		if onlyResponsive && !e.Responsive() {
			continue
		}
		r.Counted++
		info, err := record.Parse(e.Node())
		if err != nil {
			r.Malformed++
		}
		if info.IP4.IsValid() {
			r.IPv4++
			ips[info.IP4.String()]++
			if info.IP4.IsPrivate() || info.IP4.IsLoopback() || info.IP4.IsLinkLocalUnicast() {
				r.Private++
			}
		}
		if info.IP6.IsValid() {
			r.IPv6++
		}
		for _, k := range info.Keys {
			keys[k]++
		}
		for _, q := range info.Quirks {
			quirks[q]++
		}
		if info.Client != nil {
			clients[info.Client.Name]++
			v := info.Client.Name
			if info.Client.Version != "" {
				v += " " + info.Client.Version
			}
			versions[v]++
		}
		if info.UDP != 0 {
			ports[strconv.Itoa(int(info.UDP))]++
		}
		if info.Eth2 != nil {
			r.Consensus++
			digests[hex.EncodeToString(info.Eth2.ForkDigest[:])]++
			if info.Eth2.NextForkEpoch != ^uint64(0) {
				next[fmt.Sprintf("%x@%d", info.Eth2.NextForkVersion, info.Eth2.NextForkEpoch)]++
			}
		}
		if info.Attnets >= 0 {
			attSum += info.Attnets
			attN++
		}
		if info.CGC >= 0 {
			custody[strconv.Itoa(info.CGC)]++
		}
		for _, id := range info.Eth {
			r.Execution++
			k := fmt.Sprintf("%x/%d", id.Hash, id.Next)
			el[k]++
			elLabel[k] = string(record.ClassifyEL(id, now))
		}
	}
	r.DistinctIPs = len(ips)
	for _, n := range ips {
		r.MaxPerIP = max(r.MaxPerIP, n)
	}
	if attN > 0 {
		r.MeanAttnets = float64(attSum) / float64(attN)
	}
	r.ForkDigests = sorted(digests, func(k string) string {
		var d [4]byte
		hex.Decode(d[:], []byte(k))
		return record.DigestLabel(d)
	})
	r.NextForks = sorted(next, nil)
	r.ELForks = sorted(el, func(k string) string { return elLabel[k] })
	r.Custody = sorted(custody, nil)
	r.Keys = sorted(keys, nil)
	r.Quirks = sorted(quirks, nil)
	r.Clients = sorted(clients, nil)
	r.Versions = sorted(versions, nil)
	r.UDPPorts = sorted(ports, nil)
	r.Failures = sorted(fails, nil)
	return r
}

func failureClass(msg string) string {
	switch {
	case msg == "":
		return "unknown"
	case strings.Contains(msg, "timeout"):
		return "timeout"
	case strings.Contains(msg, "no UDP endpoint"):
		return "no endpoint"
	case strings.Contains(msg, "handshake"):
		return "handshake"
	default:
		return msg
	}
}

func sorted(m map[string]int, label func(string) string) []Count {
	out := make([]Count, 0, len(m))
	for k, n := range m {
		c := Count{Key: k, N: n}
		if label != nil {
			c.Label = label(k)
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].N != out[j].N {
			return out[i].N > out[j].N
		}
		return out[i].Key < out[j].Key
	})
	return out
}

func pct(n, total int) string {
	if total == 0 {
		return "-"
	}
	return fmt.Sprintf("%.1f%%", 100*float64(n)/float64(total))
}

// Print writes a human-readable report, listing at most top rows per
// section (0 for all).
func (r Report) Print(w io.Writer, top int) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "nodes\t%d\n", r.Total)
	fmt.Fprintf(tw, "  responsive\t%d\t%s\n", r.Responsive, pct(r.Responsive, r.Total))
	fmt.Fprintf(tw, "  failed\t%d\t%s\n", r.Failed, pct(r.Failed, r.Total))
	fmt.Fprintf(tw, "  not yet contacted\t%d\t%s\n", r.NeverTried, pct(r.NeverTried, r.Total))
	if r.Counted != r.Total {
		fmt.Fprintf(tw, "\nresponsive nodes only\t%d\n", r.Counted)
	}
	fmt.Fprintf(tw, "ipv4 / ipv6 records\t%d / %d\n", r.IPv4, r.IPv6)
	fmt.Fprintf(tw, "distinct ipv4 addresses\t%d\t(max %d nodes on one address)\n", r.DistinctIPs, r.MaxPerIP)
	fmt.Fprintf(tw, "private or loopback\t%d\n", r.Private)
	fmt.Fprintf(tw, "consensus (eth2) / execution (eth)\t%d / %d\n", r.Consensus, r.Execution)
	if r.Malformed > 0 {
		fmt.Fprintf(tw, "malformed entries\t%d\n", r.Malformed)
	}
	if r.Consensus > 0 {
		fmt.Fprintf(tw, "mean attestation subnets\t%.2f\n", r.MeanAttnets)
	}
	section := func(title string, cs []Count, total int) {
		if len(cs) == 0 {
			return
		}
		fmt.Fprintf(tw, "\n%s\n", title)
		shown := cs
		if top > 0 && len(cs) > top {
			shown = cs[:top]
		}
		for _, c := range shown {
			fmt.Fprintf(tw, "  %s\t%s\t%d\t%s\n", c.Key, c.Label, c.N, pct(c.N, total))
		}
		if rest := cs[len(shown):]; len(rest) > 0 {
			n := 0
			for _, c := range rest {
				n += c.N
			}
			if n > total {
				// Multi-valued sections such as ENR keys: shares do not add up.
				fmt.Fprintf(tw, "  (%d more)\n", len(rest))
			} else {
				fmt.Fprintf(tw, "  (%d more)\t\t%d\t%s\n", len(rest), n, pct(n, total))
			}
		}
	}
	section("fork digests", r.ForkDigests, r.Consensus)
	section("announced next forks (version@epoch)", r.NextForks, r.Consensus)
	section("execution fork ids (hash/next)", r.ELForks, r.Execution)
	section("peerdas custody group count", r.Custody, r.Consensus)
	section("self-reported clients (\"client\" entry)", r.Clients, r.Counted)
	section("client versions", r.Versions, r.Counted)
	section("non-standard record encodings", r.Quirks, r.Counted)
	section("enr keys", r.Keys, r.Counted)
	section("udp ports", r.UDPPorts, r.Counted)
	section("last error of failed nodes", r.Failures, r.Failed)
	return tw.Flush()
}
