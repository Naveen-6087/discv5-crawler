# discv5-crawler

A crawler for Ethereum's Node Discovery v5 DHT. It walks the routing tables of every node it can reach, keeps one record per node in a resumable database, and reports what the network is made of: fork digests, execution-layer fork IDs, PeerDAS custody, subnets, ports, and failure modes.

The wire protocol is go-ethereum's `p2p/discover` implementation, so sessions, handshakes and record verification come from the same code geth runs. This repository adds the crawl strategy, persistence, deduplication, error handling, and analysis on top.

## Layout

| Path | Contents |
| --- | --- |
| `crawler/` | Breadth-first crawl engine, retries, error classes, live counters |
| `nodedb/` | JSON node database: deduplication by node ID and sequence number, atomic writes |
| `record/` | Decoding of `eth2`, `eth`, `attnets`, `syncnets`, `cgc`, `nfd` and the IP/port entries; mainnet fork labels |
| `stats/` | Network statistics report (text or JSON) |
| `cmd/discv5-crawler/` | CLI: `crawl`, `stats`, `export` |
| `docs/` | Results of a mainnet crawl |

## Usage

```bash
go install github.com/Naveen-6087/discv5-crawler/cmd/discv5-crawler@latest
```

```bash
discv5-crawler crawl -db nodes.json -duration 10m
```

This starts from the mainnet discv5 bootnodes that ship with go-ethereum. Use `-bootnodes file` to supply your own (one ENR per line). The command logs progress every 15 seconds, writes the database every 30 seconds and on exit, and prints a report at the end. Ctrl-C stops the crawl cleanly.

```bash
discv5-crawler stats -db nodes.json -responsive
discv5-crawler stats -db nodes.json -json > report.json
discv5-crawler export -db nodes.json -responsive > live-enrs.txt
```

Running `crawl` again with the same database resumes from it. Every stored node that has not been tried for `-recheck` (30 minutes by default) is queued again. Nodes that have failed five times in a row are skipped.

## How it works

**Crawl strategy.** Each node is visited once per run:

1. **Liveness.** A `FINDNODE` at distance 0 (`RequestENR`) checks that the node is alive and fetches its current record.
2. **Bucket sweep.** The crawler then queries the node's table one log-distance at a time, from 256 downwards. It asks for a single distance per request because a NODES reply carries at most 16 records: asking for several distances at once would truncate the result. The sweep stops after three consecutive empty buckets or 16 distances. For random IDs that is well past the point where buckets are still populated.
3. **Queueing.** Every record returned is stored and, if not seen yet in this run, queued. A pool of workers (256 by default) processes the queue. The run ends when the queue is empty and every worker is idle, or when `-duration` expires.

**Deduplication.** The database is keyed by node ID. A record replaces the stored one only if its sequence number is higher, so stale copies circulating in other nodes' tables never overwrite a newer record. The record a node returns about itself is preferred over what others say about it. Every entry tracks:

- first and last seen
- last successful contact and last attempt
- consecutive failures and the last error
- how often other nodes returned it

**Error handling.**

- **Timeouts** are retried with exponential backoff (`-retries`, 1 by default). Other errors are not retried.
- **Unreachable nodes.** A node that never answers is marked failed, with its error.
- **Impostors.** A node that answers with a different identity is rejected and its record is not stored.
- **Missing endpoint.** Records without a UDP endpoint are recorded as failed without sending anything.
- **Cancellation.** A request interrupted by shutdown is not counted against the node.
- **Partial replies.** A NODES reply can span several packets. When some of them are lost, go-ethereum returns a timeout together with the records that did arrive. Those records are signed and valid, so the crawler keeps them and counts the reply as partial. On mainnet this happened to most multi-packet replies (see [docs](docs/mainnet-crawl-2026-10-11.md)). Treating them as failures would have abandoned nearly every bucket sweep.
- **Persistence.** The database is written to a temporary file and renamed into place, so an interrupted write never corrupts it. On load, every record's signature is verified again and checked against its stored ID and sequence number.

**Record decoding.**

- **Fork digests** are labelled using the mainnet genesis validators root and fork versions. From Fulu on, the EIP-7892 digest XORs in the hash of the active blob parameters, which is how `fulu`, `fulu-bpo1` and `fulu-bpo2` are told apart. The `fulu-bpo2` value was checked against the live network: it was the digest of 73% of responsive consensus nodes.
- **Execution fork IDs** (EIP-2124) are checked against go-ethereum's mainnet chain config. Each is classified as current, compatible but stale or ahead, or another network.
- **Non-standard encodings.** Two non-standard but decodable encodings seen on mainnet are accepted and reported as quirks:
  - an `eth` entry wrapped in an extra byte string, published by OP Stack clients
  - byte vectors such as `eth2` and `attnets` encoded as lists of one-byte integers
- **Malformed entries.** An entry that cannot be decoded is reported as malformed without hiding the rest of the record.

## Results

A ten-minute crawl on 2026-10-11 found 101,868 node records. 14,454 nodes answered. Among the responsive consensus-layer nodes:

- 73% were on the current mainnet fork (`fulu-bpo2`).
- 23% advertised a custody group count of 128, meaning they are PeerDAS supernodes.
- 40% advertised a custody group count of 4, the minimum.

The full report and its caveats are in [docs/mainnet-crawl-2026-10-11.md](docs/mainnet-crawl-2026-10-11.md).

## Testing

```bash
go test ./...
go test ./record -run '^$' -fuzz FuzzParse -fuzztime 60s
go test ./... -run '^$' -bench .
```

- **`record`.**
  - The EIP-778 example record and a go-ethereum mainnet bootnode record.
  - The mainnet fork digests from phase0 to electra, plus `fulu-bpo2` as observed on the live network.
  - Execution fork-ID classification.
  - Synthetic records carrying every decoded entry, both quirks, and malformed values.
  - A fuzzer that must never panic.
- **`nodedb`.**
  - Sequence-number deduplication.
  - Contact and failure bookkeeping.
  - Recheck scheduling.
  - A persistence round trip with no leftover temporary files.
  - Rejection of corrupted, tampered and wrong-version files.
- **`crawler`, simulated network.** Signed records for up to 300 nodes:
  - Every reachable node is found, and down nodes are marked failed.
  - Retries are counted, and partial replies are kept.
  - Newer records and impostors are handled.
  - The worker limit holds, the sweep stops early, cancellation is prompt, and runs resume from the database.
- **`crawler`, real network.** 25 real go-ethereum discv5 nodes on 127.0.0.1, each knowing only three others:
  - The crawler must find all of them.
  - It must pick up a record change on the next run.
  - It must mark a node that shut down as failed.
- **`cmd`.** An end-to-end `crawl`, `stats -json` and `export` against a local network, plus argument errors.

## Limitations

- **One endpoint per node.** For a dual-stack record, go-ethereum picks one endpoint: IPv4, unless the IPv6 address is more public. The other address family of dual-stack nodes is never measured. IPv6-only nodes are contacted over IPv6 when the host has IPv6 connectivity. Five of them answered in the mainnet crawl.
- **Single process.** The crawler runs as one process with one node key. Very large crawls would benefit from several vantage points and a rate limit per remote IP. Neither is implemented.
- **One JSON file.** The database is a single JSON file, rewritten on every flush. That works comfortably at the 100k-record scale seen on mainnet, but is not meant for long-term archival.
- **Mainnet labels only.** Only mainnet fork digests and fork IDs are labelled.

## License

MIT
