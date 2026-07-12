# Mainnet crawl, 2026-10-11

A ten-minute crawl from a home internet connection on Windows, with the default settings (256 workers, 700 ms timeout, one retry, mainnet bootnodes):

```bash
discv5-crawler crawl -db nodes.json -duration 10m
discv5-crawler stats -db nodes.json -responsive -top 8
```

Final progress line:

```
elapsed=10m1s known=101868 queued=101818 contacted=14454 unreachable=4514 requests=229577 buckets=181212 aborted=5198 partial=113660 new=101848 dup=1192863 errors="map[invalid response:34 timeout:33877]"
```

Report (sections counted over the 14,454 nodes that answered):

```
nodes                101868
  responsive         14454  14.2%
  failed             4514   4.4%
  not yet contacted  82900  81.4%

responsive nodes only               14454
ipv4 / ipv6 records                 14449 / 932
distinct ipv4 addresses             10738  (max 46 nodes on one address)
private or loopback                 6
consensus (eth2) / execution (eth)  5492 / 6724
mean attestation subnets            7.79

fork digests
  8c9f62fe   fulu-bpo2  3993  72.7%
  c6ecb76c              386   7.0%
  669e6c11              213   3.9%
  845ee52a              190   3.5%
  3237dab6              172   3.1%
  98c910cf              81    1.5%
  74d01459              65    1.2%
  aadde438              58    1.1%
  (45 more)             334   6.1%

announced next forks (version@epoch)
  00000000@0         187  3.4%
  42000006@412672    17   0.3%
  7000101b@4096      17   0.3%
  70000910@50688     15   0.3%
  42010007@412672    7    0.1%
  80000910@132352    7    0.1%
  70000910@52480     6    0.1%
  01000000@74240     4    0.1%
  (6 more)           9    0.2%

execution fork ids (hash/next)
  07c9462e/0  mainnet (current)  3187  47.4%
  4be0e445/0  other network      458   6.8%
  9d4df79a/0  other network      361   5.4%
  22d523b2/0  other network      356   5.3%
  23aa1351/0  other network      283   4.2%
  6c1d9423/0  other network      235   3.5%
  62ebcfaf/0  other network      186   2.8%
  d38cc282/0  other network      155   2.3%
  (238 more)                     1503  22.4%

peerdas custody group count
  4            2187  39.8%
  128          1274  23.2%
  8            822   15.0%
  64           204   3.7%
  0            67    1.2%
  10           34    0.6%
  9            28    0.5%
  20           21    0.4%
  (95 more)    337   6.1%

non-standard record encodings
  eth entry wrapped in a byte string    204  1.4%
  eth2 encoded as a list of bytes       3    0.0%
  attnets encoded as a list of bytes    1    0.0%

enr keys
  id           14454  100.0%
  secp256k1    14454  100.0%
  udp          14452  100.0%
  ip           14449  100.0%
  tcp          14178  98.1%
  eth          6724   46.5%
  eth2         5492   38.0%
  attnets      5471   37.9%
  (29 more)

udp ports
  30303          4088  28.3%
  9000           2810  19.4%
  12000          822   5.7%
  9222           532   3.7%
  9001           265   1.8%
  12001          247   1.7%
  9010           226   1.6%
  30304          212   1.5%
  (1256 more)    5250  36.3%

last error of failed nodes
  timeout                                  4480  99.2%
  0 nodes in response for distance zero    34    0.8%
```

Notes:

- **Coverage.** The crawl did not finish. 82,900 records were still queued when the time ran out. Most of them are stale records of nodes that are gone, because records keep circulating in other nodes' tables. The responsive count is therefore a lower bound on the live network.
- **Other networks.** discv5 is shared by every network that uses it. Fork digests without a label are other consensus networks: testnets, Gnosis, and devnets. Execution fork IDs marked "other network" are other chains, including OP Stack and BSC. Only mainnet digests and fork IDs are labelled.
- **Partial replies.** About 63% of successful FINDNODE queries (113,660 of 181,212) arrived incomplete: the first packets of a multi-packet NODES reply came in, and the rest did not before the timeout. The crawler keeps the records that did arrive. Changing the socket buffer size made no difference on this machine, so the loss happens on the network path or at the remote side.
