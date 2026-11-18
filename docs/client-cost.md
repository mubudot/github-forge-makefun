# Client cost audit

This investigation did not establish a server-controlled explanation for the
reported roughly 20% production gap. The retained change removes an unused REST
request-ID header from successful binary Git replies. It saves bytes and header
allocations, but the end-to-end measurements do not establish a material speedup.
REST replies, issue shapes, Git objects, protocol negotiation, and transport
defaults retain their existing behavior.

[Recorded measurements](client-cost-results.json) include individual trial
rounds, client CPU/GC, summary statistics, and syscall counts.

## What the client actually measures

The kit's `program/env/src/bench.ts`, `wire.ts`, `main.ts`, `git-client.ts`, and
`rest-client.ts` were traced through their call sites. Their SDK copies have the
same hashes. The shipped `program/env/server.mjs` confirms the same operations.

| Workload | Requests in one timed round | Server-controlled bytes and parsing |
| --- | ---: | --- |
| Push, 200 serial | 400: advertisement GET, receive-pack POST per operation | Advertisement 140 B; report 50 B. One branch, three capability tokens, one outer band-1 packet containing the required inner report packets. No progress. Incoming three-object packs are generated before timing. |
| Clone, 50 concurrent | 250: three advertisement GETs, ls-refs POST, fetch POST per clone | Advertisements 55 B each; ls-refs 145 B; fetch 566 B. Total 876 B per clone / 43,800 B per round, before HTTP headers. |
| Read, 2,000 serial | 2,000 GETs | Issue JSON 527 B after conformance, 1,054,000 B per round. Fully buffered, copied to an ArrayBuffer, decoded as UTF-8, then JSON parsed. |
| Write, 200 serial | 200 POSTs | Plausible complete issue objects, 377–547 B; about 94–95 KB per round. Request JSON is 101–270 B. Responses are fully buffered, decoded and JSON parsed, even though the timed assertion only needs status 201. |

`clone()` asks for an advertisement, calls `lsRefs()` which asks again, and then
calls `fetch()` which asks again. The five requests are issued by the fixed client.
Honoring its v2 request is required by the public contract and conformance. Sending
v0 selectively to change that path would change the required behavior.

The depth-one pack is **470 B**, with three plain objects: commit 235 B, tree
39 B, blob 512 B uncompressed. All use zlib `78 9c` streams from the client's
default-compression push. There are no deltas or unnecessary historical objects.
The fetch uses one band-1 packet plus shallow-info and the required section/control
packets. **Timed clones use `parse: false`: they verify the SHA-1 pack checksum, but do not
inflate or SHA-1 each object.** Object inflation and object hashes belong to the
untimed conformance checks. Repacking, introducing deltas, and changing object
compression cannot remove a large timed client inflate cost here.

## Connections, framing, and allocation

All workload clients use Node's shared global fetch/undici pool. Constructing a
new `GitClient` or `RestClient` does not create a private connection pool. Wire
diagnostics after the entire conformance suite show:

- Push/read/write use two persistent sockets, with zero new connections or
  transport errors in their timed rounds. The second socket is consistent with
  undici's deferred HTTP/1 completion/drain handling; there is no per-operation
  reconnect problem.
- Clone's first timed round opens 97–98 sockets and grows the pool to about 100.
  The following two rounds reuse those sockets without new connections. The five
  serial warm-up clones do not warm 50 concurrent clones' connection demand.
- Every response has an accurate Content-Length; the server writes each buffered
  workload response in one call. TCP can still fragment that write. HTTP/1.1
  persistence needs no explicit Connection header. No
  Transfer-Encoding, Content-Encoding, redirect, cookie, ETag, or HTTP trailer
  invokes another client path. No HTTP compression creates zlib worker work.
- Without a Keep-Alive hint, this undici version uses its default four-second
  idle timeout. A hint is reduced by its two-second safety threshold; timeout=2
  would make it discard sockets after every response. Timeout=3 was tested and
  discarded: it did not reduce read CPU/GC. Pool client objects survive ordinary
  socket disconnection.
- Content-Length mismatch, an EOF-delimited body, Connection: close, early
  termination, or a response completed while a request body is still being
  written can discard a socket. None occurred in the baseline workload trace.
- MCP uses stateless JSON responses, with no lingering GET/SSE session or
  background client poll. The wire count contains exactly the workload requests.
  Entry `/done` polling is separate, with an exponential delay capped at two
  seconds; it does not run per benchmark operation.

These paths were checked against Node 22.23.5's bundled undici 6.29.1 and the
[matching HTTP/1 implementation](https://github.com/nodejs/undici/blob/v6.29.1/lib/dispatcher/client-h1.js).
Its [fetch body implementation](https://github.com/nodejs/undici/blob/v6.29.1/lib/web/fetch/body.js)
copies arrayBuffer responses. The client's TextDecoder and JSON.parse add their
own representations. Thus JSON payload size affects copying and parsing, but it
is only part of fetch's per-request allocation. Fifty timed clone pack buffers
contain just 23.5 KB of pack bytes; their HTTP/stream/request objects cost much
more than their Git data.

A constrained diagnostic SDK client profile found about 20% of read wall samples in GC,
12% in socket writes, and 2% in JSON.parse. It is a sampled profile, not CPU
accounting. Direct process CPU accounting is reported separately below.

## Experiments and retained change

Only Git request-ID removal is retained. Successful Git responses go from three
headers to two. In a full trace this saves 10,400 B per push round and 6,500 B per
clone round, excluding status lines: **1,950 parsed header pairs and 50,700 B per
three-round benchmark**. The client also avoids allocating/lowercasing that
unknown header name and storing its value. All REST responses retain a non-empty
X-GitHub-Request-Id, as the rules require. Git error JSON retains one too.

Discarded experiments include explicit long keep-alive hints, chunked output,
separate header/body writes, leaner JSON, shorter/case-changed HTTP headers,
shorter idle hints, removing optional unborn capability advertising, netpoll,
and a two-blocking-connection/netpoll hybrid. None established the requested
systematic improvement. In 60 constrained read rounds per variant, dropping
dates and user details gave 504.54 -> 505.09 ms median client CPU; a minimal issue
body gave 510.00 ms. Trimming those fields was therefore discarded. Removing the
required REST request-ID was a diagnostic ceiling only, never a candidate.

## Measurement method and results

`scripts/measure-client.mjs` imports the **sealed JavaScript env**, substitutes
only its command-line invocation, and brackets its existing timed round loops.
Conformance, fixtures, clients, warm-ups, three rounds, assertions and scoring
are unchanged. It records process user/system CPU, GC, RSS, and event-loop
counters. Optional diagnostics count sockets, exchanges, headers and body bytes;
those diagnostic runs are excluded from speed comparisons. Syscall runs are
also excluded from speed comparisons. `scripts/count-syscalls.mjs` now reads
these exact round windows and the env's TIDs.

Local measurements use two CPUs and Node
`--max-old-space-size=256 --max-semi-space-size=1`, matching the rig client's
approximately 259 MiB V8 heap limit and small young generation. This is a V8
constraint, not a claim of a local 256 MiB cgroup. Six fresh client/server runs
per implementation alternate order. Values below are the median of run p50s
for wall time and the median timed-round process CPU for CPU time.
Process CPU includes all env threads, including concurrent GC workers, so it
can exceed wall time on two CPUs.

| Workload | Local wall ms before -> after | Local client CPU ms before -> after | Client syscalls/op before -> after |
| --- | ---: | ---: | ---: |
| Push | 348.04 -> 317.68 | 400.51 -> 368.87 | 11.08 -> 11.56 |
| Clone | 122.97 -> 120.44 | 155.28 -> 149.71 | 27.63 -> 27.82 |
| Read | 575.65 -> 567.13 | 721.14 -> 718.15 | 5.11 -> 5.01 |
| Write | 101.78 -> 112.82 | 120.25 -> 125.93 | 6.40 -> 5.86 |

Geomean of the four local workload estimates: **223.77 -> 222.44 ms**. Header
removal does not remove network exchanges or socket syscalls. GC/futex counts
and timed idle socket closure account for much of the variation in total calls;
the table is not evidence of a syscall reduction.

The InstaBox Docker rig uses runsc-netstack, entry 2 CPUs / 1 GiB and env
2 CPUs / **256 MiB**, with the kit's runsc PID adjustment. Eight fresh sealed
env trials per implementation include conformance and exactly three rounds,
one entry and one client per trial. Every trial passes 120/120 and exports the
required state hash.

| Workload | Rig wall ms before -> after | Rig client CPU ms before -> after |
| --- | ---: | ---: |
| Push | 305.14 -> 349.46 | 325 -> 330 |
| Clone | 134.91 -> 124.62 | 155 -> 145 |
| Read | 758.55 -> 773.27 | 750 -> 740 |
| Write | 120.35 -> 118.09 | 115 -> 115 |

Rig geomean: **247.60 -> 251.12 ms**. Client RSS peaks at 175.42 MiB before,
173.39 MiB after. This does not establish a performance improvement.

A supplemental five-pair run put Go and the sealed client in one gVisor sandbox.
It used a shared 2-CPU/256-MiB container, so it does not reproduce production's
separate member quotas. Geomeans were 593.81 -> 585.59 ms. Another shared-sandbox
netpoll comparison did not establish a consistent gain. Both shared-sandbox
results are sensitive to resource sharing and host drift and are not production
predictions.

Earlier 21-round interleaving was unsuitable as the primary memory comparison:
it retained two implementations' prebuilt 4,205-push chains and their pools in
one env, reaching about 222 MiB RSS. Production prebuilds 605 pushes and uses
one entry/client. Loading the TS SDK also adds a compiler/WASM heap absent from
the sealed env. These were corrected in the fresh-client measurements above.

## Final validation

The native working tree passes `go test ./...`, `go vet ./...`, and all 16 Node
tests. Two native builds produce identical amd64 bytes. The official process
grade uses `--worktree` because the CLI otherwise grades HEAD, and temporarily
includes the normally ignored ARM64 executable so it exercises Go on this host.
Its result is feature **600**, agent **200**, 120/120 checks, replay matched.
The final Docker/runsc grade also scores 600/200 and matches replay. Both export:

```
a0e98d49854b961230227867235c8615858e006db3763f341ac6154af1445590
```

The original ARM64 ignore rule is restored. The InstaBox machine is suspended.

The remaining dominant client costs are fetch/streams/abort handling, GC, and
socket scheduling for the required exchanges. The audit found neither excess
pack objects nor connection churn nor oversized responses that explain a
reliable 20% deficit. Confidence is high in the wire reduction and low in a
meaningful production speed gain.

## Reproduce

Run from this checkout; outputs remain under /tmp and the kit is unmodified.

```sh
KIT=../github-core-clone-01-kit
OUT=/tmp/forge-client-audit
./scripts/build-native.sh

# Exact sealed suite, client CPU/GC, fresh process.
taskset -c 0,1 node --max-old-space-size=256 --max-semi-space-size=1 \
  scripts/measure-client.mjs "$KIT" "$OUT/cpu" bin/forge-linux-arm64

# Wire diagnostics: use the executable matching this machine's architecture.
FORGE_MEASURE_WIRE=1 taskset -c 0,1 node \
  --max-old-space-size=256 --max-semi-space-size=1 \
  scripts/measure-client.mjs "$KIT" "$OUT/wire" bin/forge-linux-arm64

# Client-only syscall classification from the full process-tree trace.
taskset -c 0,1 strace -f -ttt -qq -s 0 -o "$OUT.trace" \
  node --max-old-space-size=256 --max-semi-space-size=1 \
  scripts/measure-client.mjs "$KIT" "$OUT/trace" bin/forge-linux-arm64
node scripts/count-syscalls.mjs "$OUT/trace" "$OUT.trace"
```

Use `bin/forge-linux-amd64` on amd64. Omitting the executable exercises `main.ts`
and its selected core. `FORGE_MEASURE_SDK=1` intentionally switches back to the
SDK for comparison. Raw local/rig data and development variants are under
`/tmp/forge-client-cost`; nothing there is part of the entry.
