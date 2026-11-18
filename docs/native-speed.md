# Native core measurements

The entry now uses a static Go core with a Node supervisor and the preserved
TypeScript fallback. Measurements here use only this checkout, the supplied
`github-core-clone-01-kit`, Node 22.23.5, and Go 1.25.4 on Linux ARM64.
Production Linux amd64 under gVisor still needs the operator's separate test.
Local absolute times do not establish the requested production target below 350 ms.

## Correctness and replay

The first native `grade-local --driver process --worktree` run scored feature
600, agent 200, speed 200, total 1000, with a 136.547 ms workload geomean.
Its 120/120 checks include all 15 MCP checks. The live and replay state exports
match byte for byte with SHA-256:

```
a0e98d49854b961230227867235c8615858e006db3763f341ac6154af1445590
```

The final unprofiled official grade covers all three practice instances, live and
replay. Every instance scores 600/200/200 and has this same state hash. Their
geomeans are 134.830, 132.860, and 149.076 ms (mean 138.922 ms). All 16 Node tests,
the Go unit tests, and `go vet ./...` pass. Tests cover delta chains and thin packs,
atomic rejection and inflater recovery, HTTP fragmentation/chunks/pipelining and
backpressure, the connection thread limit, shared protocol state, invalidation,
lifecycle retries, signal forwarding, and unavailable/damaged binary fallback.

The untouched TS tree was copied to `/tmp/forge-ts` before changes. Its official
baseline also passes 120/120, scores 600/200/200, and replays with this hash.
An initial attempted native grade exercised fallback because the kit excludes
ignored files, including the local ARM64 executable. Native official grades
therefore temporarily remove that one ignore rule and restore it afterward.
The submitted tree contains only the amd64 executable.

## Alternating comparisons

`compare-native.mjs` runs the unmodified SDK conformance suite and all four
workloads, three rounds per workload, in a fresh process per variant. Variant
orders alternate between rounds; no compilation, tests, or profiling run alongside
the measurements. The table is the median of three per-run workload medians from
the final transport comparison. Both variants use the same unchanged TS fallback
code. Every run passes 120/120 with the hash above.

| Local workload, ms | TypeScript | Go blocking | Go netpoll |
| --- | ---: | ---: | ---: |
| Push, 200 sequential | 343.801 | 256.415 | 255.301 |
| Clone, 50 concurrent | 64.654 | 78.801 | 66.260 |
| Read, 2,000 sequential | 495.379 | 433.187 | 444.703 |
| Write, 200 sequential | 76.363 | 72.975 | 83.327 |
| Geomean of these medians | 170.287 | 158.976 | 158.230 |

Blocking Go improves this local geomean by 6.6% against TS. An earlier three-run
comparison measured 172.076 ms TS, 156.328 ms blocking Go, and 157.874 ms netpoll
Go. The two Go transports are close overall locally; workload tradeoffs and host
variation are larger than their overall difference. The default remains blocking
I/O to avoid the netpoll readiness/EAGAIN/epoll path on sequential requests.
`FORGE_TRANSPORT=netpoll` preserves the comparison option for gVisor testing.
Peak process RSS in the final comparison was 29,096 KiB for blocking Go,
28,072 KiB for netpoll Go, and 72,308 KiB for TS.

Retained changes:

- Synchronous blocking socket reads/writes with one connection goroutine and a
  locked OS thread. A limit of 112 blocking connections leaves pid headroom;
  additional connections use netpoll. TCP_NODELAY is set once. The netpoll
  listener disables TCP keepalive at creation to avoid redundant socket options.
- Reusable request/output buffers; a response is contiguous, with a content
  length and request ID. Complete pipelined requests can share one write.
  Partial writes, chunked input, fragmentation, and backpressure remain supported.
- Cached issue JSON, parsed routes and header templates, advertisements, original
  compressed Git entries, fetch/shallow replies, and receive-pack reports.
  Mutations invalidate the affected caches. Cache keys come from actual requests
  and repository state, with no workload names or suite shortcuts in server code.
- Reusable zlib readers and atomic pack publication. The decoder verifies the
  zlib trailer after reading the declared output length, in addition to the
  outer pack checksum and delta bounds.
- GOMAXPROCS 2, GOGC 500, and a 600 MiB soft memory limit.

Cached issue dispatch improved from 295–317 ns, 160 bytes and four allocations
per operation to 87–88 ns and zero allocations in its Go microbenchmark.
Alternating cached Git request headers take about 91 ns and zero allocations.
A three-object pack microbenchmark takes about 10 microseconds per decode.
These measurements isolate CPU/allocation work; they are not scored workload times.

Discarded experiments: a nonblocking raw send that bypassed Go's scheduler did
not establish a repeatable improvement, and removing connection thread locks did
not improve overall times. The corresponding switches and raw-send code were
removed. A noisy comparison window ranged from 155 to 366 ms between runs, so no
production speed claim is based on its absolute times.

## Profiling and reproduction

The official process-driver grade was also run with `FORGE_CPU_PROFILE` and
`FORGE_HEAP_PROFILE` passed through `--dotenv`, writing profiles under `/tmp`.
That grade passes 120/120 and replay matches. Its profiled times are excluded from
the comparison above. A replay CPU profile records 280 ms of samples over 5.78 s;
socket syscalls and runtime syscall/scheduling work lead the profile, with pack
inflation a smaller cost. The sample is coarse: it supports prioritizing I/O,
not a precise split of end-to-end latency or a measured total syscall count.

```sh
./scripts/build-native.sh
node --test test/*.test.ts
(cd native && GOCACHE=/tmp/forge-go-cache GOPATH=/tmp/forge-go-path go test ./...)

KIT=../github-core-clone-01-kit
node scripts/compare-native.mjs "$KIT" /tmp/forge-native-compare 3
FORGE_CPU_PROFILE=/tmp/forge-cpu.pprof \
FORGE_HEAP_PROFILE=/tmp/forge-heap.pprof \
node scripts/measure-speed.mjs "$KIT" /tmp/forge-native-profile
/workspace/tools/go/bin/go tool pprof -top /tmp/forge-cpu.pprof
```

For official ARM64 grades, temporarily unignore `bin/forge-linux-arm64`, run from
the kit, and restore `.gitignore` afterward. Supply `--instances 3` to cover every
practice instance. Profiling variables can be supplied in a temporary `.env`
using the kit's `--dotenv`; leave these unset for timing runs. This also verifies
that profile output is optional and the ordinary entry never writes its repository.

All measurement artifacts, profiles and local TS copies remain under `/tmp`.
No sandbox rig, credentials, third-party module, or generated build cache is
part of the entry. The native build is reproducible, stripped, and statically
linked; only its Linux amd64 output is intended for the submitted snapshot.
Two consecutive builds produced identical SHA-256 hashes for both architectures.
The amd64 executable is 6,795,412 bytes (6.48 MiB); ARM64 is 6,357,140 bytes
(6.06 MiB, ignored). The submitted file set is about 6.74 MiB total, below the
8 MiB per-file and 16 MiB tree limits. `file` reports both outputs as stripped,
statically linked Linux ELF executables; Go build metadata confirms CGO is disabled.
