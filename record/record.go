package record

import (
	"encoding/binary"
	"fmt"
	"math/bits"
	"net/netip"
	"slices"

	"github.com/ethereum/go-ethereum/core/forkid"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/p2p/enr"
	"github.com/ethereum/go-ethereum/rlp"
)

// Eth2 is the consensus-layer ENRForkID (the "eth2" entry).
type Eth2 struct {
	ForkDigest      [4]byte
	NextForkVersion [4]byte
	NextForkEpoch   uint64
}

// Info is everything the crawler extracts from a record.
type Info struct {
	ID   enode.ID
	Seq  uint64
	IP4  netip.Addr
	IP6  netip.Addr
	UDP  uint16
	TCP  uint16
	QUIC uint16
	Keys []string

	Eth2     *Eth2
	Attnets  int // subscribed attestation subnets, -1 if absent
	Syncnets int // subscribed sync committee subnets, -1 if absent
	CGC      int // PeerDAS custody group count, -1 if absent
	NFD      *[4]byte

	Eth []forkid.ID // execution-layer fork IDs from the "eth" entry

	Quirks []string // non-standard but decodable encodings

	Client *Client // from the optional "client" entry
}

// Client is the self-reported implementation in the "client" entry, an RLP
// list [name, version, ...]. Lighthouse and Grandine publish it.
type Client struct {
	Name    string
	Version string
}

func loadClient(n *enode.Node) (*Client, error) {
	var fields []string
	if err := n.Load(enr.WithEntry("client", &fields)); err != nil {
		return nil, err
	}
	if len(fields) == 0 || fields[0] == "" {
		return nil, fmt.Errorf("empty client entry")
	}
	c := &Client{Name: fields[0]}
	if len(fields) > 1 {
		c.Version = fields[1]
	}
	return c, nil
}

// QuirkWrappedEth marks an "eth" entry whose list is wrapped in an extra RLP
// byte string. Some OP Stack execution clients publish records like this.
const QuirkWrappedEth = "eth entry wrapped in a byte string"

// QuirkByteList is appended to the key of a byte-vector entry (eth2,
// attnets, ...) that was encoded as an RLP list of one-byte integers.
const QuirkByteList = " encoded as a list of bytes"

func loadEth(n *enode.Node) (id forkid.ID, wrapped bool, err error) {
	var raw rlp.RawValue
	if err := n.Load(enr.WithEntry("eth", &raw)); err != nil {
		return id, false, err
	}
	kind, content, _, err := rlp.Split(raw)
	if err != nil {
		return id, false, err
	}
	if kind == rlp.String {
		raw, wrapped = content, true
	}
	var e ethEntry
	if err := rlp.DecodeBytes(raw, &e); err != nil {
		return id, wrapped, err
	}
	return e.ForkID, wrapped, nil
}

// ethEntry mirrors go-ethereum's "eth" ENR entry: a list whose first element
// is the fork ID, with room for future fields.
type ethEntry struct {
	ForkID forkid.ID
	Rest   []rlp.RawValue `rlp:"tail"`
}

type rawEntry struct {
	key    string
	val    []byte
	asList bool
}

func (r rawEntry) ENRKey() string { return r.key }

func (r *rawEntry) DecodeRLP(s *rlp.Stream) error {
	kind, _, err := s.Kind()
	if err != nil {
		return err
	}
	if kind != rlp.List {
		r.val, err = s.Bytes()
		return err
	}
	// Some implementations encode fixed-size byte vectors as a list of
	// one-byte integers.
	var ints []uint64
	if err := s.Decode(&ints); err != nil {
		return err
	}
	r.val = make([]byte, len(ints))
	for i, v := range ints {
		if v > 0xff {
			return fmt.Errorf("list element %d is %d, not a byte", i, v)
		}
		r.val[i] = byte(v)
	}
	r.asList = true
	return nil
}

// loadBytes reads a byte-string entry. A missing key is not an error.
func loadBytes(n *enode.Node, key string) (val []byte, asList bool, err error) {
	e := rawEntry{key: key}
	if err := n.Load(&e); err != nil {
		if enr.IsNotFound(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return e.val, e.asList, nil
}

// recordKeys lists the keys of a record. AppendElements yields
// [seq, k1, v1, k2, v2, ...] with keys already sorted.
func recordKeys(n *enode.Node) []string {
	el := n.Record().AppendElements(nil)
	var keys []string
	for i := 1; i+1 < len(el); i += 2 {
		if k, ok := el[i].(string); ok {
			keys = append(keys, k)
		}
	}
	return keys
}

// Parse decodes n. Malformed optional entries are reported in the error but do
// not prevent the rest of the record from being read.
func Parse(n *enode.Node) (Info, error) {
	info := Info{ID: n.ID(), Seq: n.Seq(), Attnets: -1, Syncnets: -1, CGC: -1}
	var errs []error

	var ip4 enr.IPv4
	if n.Load(&ip4) == nil {
		info.IP4, _ = netip.AddrFromSlice(ip4)
		info.IP4 = info.IP4.Unmap()
	}
	var ip6 enr.IPv6
	if n.Load(&ip6) == nil {
		info.IP6, _ = netip.AddrFromSlice(ip6)
	}
	var udp enr.UDP
	if n.Load(&udp) == nil {
		info.UDP = uint16(udp)
	}
	var tcp enr.TCP
	if n.Load(&tcp) == nil {
		info.TCP = uint16(tcp)
	}
	var quic enr.QUIC
	if n.Load(&quic) == nil {
		info.QUIC = uint16(quic)
	}

	info.Keys = recordKeys(n)
	has := func(k string) bool { return slices.Contains(info.Keys, k) }
	bytesEntry := func(k string) []byte {
		b, asList, err := loadBytes(n, k)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", k, err))
		}
		if asList {
			info.Quirks = append(info.Quirks, k+QuirkByteList)
		}
		return b
	}

	if b := bytesEntry("eth2"); b != nil {
		if len(b) < 16 {
			errs = append(errs, fmt.Errorf("eth2: %d bytes, want 16", len(b)))
		} else {
			e := &Eth2{NextForkEpoch: binary.LittleEndian.Uint64(b[8:16])}
			copy(e.ForkDigest[:], b[:4])
			copy(e.NextForkVersion[:], b[4:8])
			info.Eth2 = e
		}
	}
	if has("attnets") {
		info.Attnets = popcount(bytesEntry("attnets"))
	}
	if has("syncnets") {
		info.Syncnets = popcount(bytesEntry("syncnets"))
	}
	if b := bytesEntry("nfd"); len(b) == 4 {
		var d [4]byte
		copy(d[:], b)
		info.NFD = &d
	}
	if has("cgc") {
		var cgc uint64
		if err := n.Load(enr.WithEntry("cgc", &cgc)); err != nil {
			errs = append(errs, fmt.Errorf("cgc: %w", err))
		} else {
			info.CGC = int(cgc)
		}
	}
	if has("client") {
		c, err := loadClient(n)
		if err != nil {
			errs = append(errs, fmt.Errorf("client: %w", err))
		} else {
			info.Client = c
		}
	}
	if has("eth") {
		id, wrapped, err := loadEth(n)
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("eth: %w", err))
		case wrapped:
			info.Quirks = append(info.Quirks, QuirkWrappedEth)
			info.Eth = append(info.Eth, id)
		default:
			info.Eth = append(info.Eth, id)
		}
	}
	if len(errs) > 0 {
		return info, fmt.Errorf("record %s: %v", n.ID().TerminalString(), errs)
	}
	return info, nil
}

func popcount(b []byte) int {
	c := 0
	for _, x := range b {
		c += bits.OnesCount8(x)
	}
	return c
}
