package record

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/forkid"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
)

// MainnetGenesisValidatorsRoot is the beacon chain genesis_validators_root.
var MainnetGenesisValidatorsRoot = mustHex32("4b363db94e286120d76eb905340fdd4e54bfe9f06bf33ff6cf5ad27f511bfe95")

type clFork struct {
	name    string
	version [4]byte
	epoch   uint64
}

// Mainnet consensus forks (consensus-specs configs/mainnet.yaml).
var mainnetForks = []clFork{
	{"phase0", [4]byte{0, 0, 0, 0}, 0},
	{"altair", [4]byte{1, 0, 0, 0}, 74240},
	{"bellatrix", [4]byte{2, 0, 0, 0}, 144896},
	{"capella", [4]byte{3, 0, 0, 0}, 194048},
	{"deneb", [4]byte{4, 0, 0, 0}, 269568},
	{"electra", [4]byte{5, 0, 0, 0}, 364032},
	{"fulu", [4]byte{6, 0, 0, 0}, 411392},
}

type blobParams struct{ epoch, maxBlobs uint64 }

// From Fulu on (EIP-7892) the digest also commits to the blob parameters in
// force. Electra's limit applies until the first blob-parameter-only fork.
var mainnetBlobSchedule = []blobParams{
	{364032, 9},
	{412672, 15},
	{419072, 21},
}

func mustHex32(s string) [32]byte {
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 32 {
		panic("bad hex32 " + s)
	}
	var out [32]byte
	copy(out[:], b)
	return out
}

// ForkDataRoot is hash_tree_root(ForkData(version, genesis_validators_root)).
func ForkDataRoot(version [4]byte, gvr [32]byte) [32]byte {
	var buf [64]byte
	copy(buf[:4], version[:])
	copy(buf[32:], gvr[:])
	return sha256.Sum256(buf[:])
}

// ForkDigest computes the pre-Fulu digest: the first four bytes of the fork
// data root.
func ForkDigest(version [4]byte, gvr [32]byte) [4]byte {
	r := ForkDataRoot(version, gvr)
	return [4]byte{r[0], r[1], r[2], r[3]}
}

// FuluForkDigest computes the EIP-7892 digest: the fork data root XORed with
// hash(uint64_le(blob epoch) || uint64_le(max blobs)), truncated to 4 bytes.
func FuluForkDigest(version [4]byte, gvr [32]byte, epoch, maxBlobs uint64) [4]byte {
	r := ForkDataRoot(version, gvr)
	var b [16]byte
	binary.LittleEndian.PutUint64(b[:8], epoch)
	binary.LittleEndian.PutUint64(b[8:], maxBlobs)
	h := sha256.Sum256(b[:])
	return [4]byte{r[0] ^ h[0], r[1] ^ h[1], r[2] ^ h[2], r[3] ^ h[3]}
}

var (
	digestOnce   sync.Once
	digestLabels map[[4]byte]string
)

// DigestLabel names a mainnet fork digest, or returns "" if it is unknown
// (another network, a devnet, or a fork this table predates).
func DigestLabel(d [4]byte) string {
	digestOnce.Do(func() {
		digestLabels = map[[4]byte]string{}
		gvr := MainnetGenesisValidatorsRoot
		for _, f := range mainnetForks {
			if f.version[0] < 6 {
				digestLabels[ForkDigest(f.version, gvr)] = f.name
			}
		}
		fulu := mainnetForks[len(mainnetForks)-1]
		bpo := 0
		for _, bp := range mainnetBlobSchedule {
			switch {
			case bp.epoch <= fulu.epoch:
				// The parameters in force when Fulu itself activated.
				digestLabels[FuluForkDigest(fulu.version, gvr, bp.epoch, bp.maxBlobs)] = fulu.name
			default:
				bpo++
				digestLabels[FuluForkDigest(fulu.version, gvr, bp.epoch, bp.maxBlobs)] = fmt.Sprintf("%s-bpo%d", fulu.name, bpo)
			}
		}
	})
	return digestLabels[d]
}

// ELForkClass classifies an execution-layer fork ID against mainnet.
type ELForkClass string

const (
	ELCurrent      ELForkClass = "mainnet (current)"
	ELCompatible   ELForkClass = "mainnet (stale or ahead)"
	ELIncompatible ELForkClass = "other network"
)

var (
	elOnce    sync.Once
	elFilter  forkid.Filter
	elGenesis *types.Block
)

func mainnetGenesis() *types.Block {
	elOnce.Do(func() {
		elGenesis = core.DefaultGenesisBlock().ToBlock()
		elFilter = forkid.NewStaticFilter(params.MainnetChainConfig, elGenesis)
	})
	return elGenesis
}

// ClassifyEL compares id with mainnet's fork ID at time now.
func ClassifyEL(id forkid.ID, now time.Time) ELForkClass {
	g := mainnetGenesis()
	cur := forkid.NewID(params.MainnetChainConfig, g, math.MaxUint64, uint64(now.Unix()))
	if id == cur {
		return ELCurrent
	}
	if elFilter(id) == nil {
		return ELCompatible
	}
	return ELIncompatible
}
