# Production speed measurement

The production entry at d081ea1 passed all 120 checks and scored 141/200 speed
points. Native timings alone are an unreliable predictor of the gVisor pod.
Measurements below keep the suite source and its inputs unchanged.

## What each timed operation does

All workloads run three rounds after five warm-up operations. Repository creation,
fixture construction, and pack compression on the client happen before timing.
There is no git subprocess: the sealed GitClient is TypeScript using Node fetch,
SHA-1, zlib, and pkt-line decoding. Clients use persistent HTTP/1.1 connections.

| Workload | Timed client behavior | Wire sizes at d081ea1 | What the forge can influence |
| --- | --- | --- | --- |
| Push | 200 serial operations. GET receive-pack info/refs, consume/decode advertisement; POST commands plus prebuilt pack, consume/demultiplex/decode report-status; verify unpack and ref result. Two HTTP exchanges/op. | GET response 140 B; POST body 720–769 B, including three new objects and a 1 KiB source blob; POST response 50 B. | HTTP dispatch, syscall/response framing, pack validation/inflate/hash/indexing, ancestry check, report allocation. Already-held-object indexing cannot accelerate this fixture: each pack has three new objects. |
| Clone | 50 concurrent depth-1 clones of a 200-commit fixture. GET capabilities; lsRefs does another GET then POST; fetch does another GET then POST. All five exchanges complete; fetch pack checksum is verified, but object parsing/inflate is disabled. | Capability response 55 B; ls-refs request 60 B / response 145 B; fetch request 142 B / response 566 B. Setup's 88,239 B history pack is untimed. | HTTP connection/packet overhead, cached advertisements/refs/depth-1 replies, output bytes, client-side copying and checksum work caused by those bytes. The five requests are fixed by the client. |
| Read | 2,000 serial GETs of one issue using the same RestClient/pool; each response is fully read, UTF-8 decoded, JSON parsed and checked. One exchange/op. | Request body 0 B, response body 527 B; 161–162 B response headers. | Lookup, serialized-response reuse, HTTP framing, socket/poll/GC cost. The client wrapper, fetch and JSON processing remain client work. |
| Write | 200 serial POST issue creations with prebuilt title/body values. Each response is fully read, decoded and parsed; status must be 201. One exchange/op. | Request body 101–270 B; response body 377–547 B. The response is an issue object, not a full repository. | Request parsing, validation, ID allocation, in-memory insertion and response serialization/framing. |

Wire sizes are bodies, excluding HTTP headers. The one native observation run
used two sockets for serial issue traffic. Concurrent clone traffic reached 99
sockets across setup/warm-up/three rounds; new connections concentrate in the
first timed clone round. There are no per-request disk writes, fsyncs, worker
spawns, or asynchronous zlib operations in the forge.

## Method

- Native syscall accounting: strace 6.13, `-f -ttt -s 0`; classify forge and suite
  TIDs and count syscall entries within each sealed timed round. Counts include
  background V8 work and failed calls. Straced timings are not speed results.
- Sandbox proxy: official runsc release-20261109.0, ARM64, systrap, network=none;
  suite and entry are inside the same sandbox and use gVisor loopback. Both share
  CPUs 0–1. Output files are under /tmp. This approximates pod transport costs,
  not the production machine's CPU or the exact separate-container quotas.
- Validation: grade-local with the process driver, worktree mode, CPUs 0–1, including
  live/replay comparison. The required all-pass state SHA-256 is
  `a0e98d49854b961230227867235c8615858e006db3763f341ac6154af1445590`.

The native socket baseline already coalesces each response into one writev;
calling res.end once does not imply a single contiguous write. It also issues a
zero-timeout epoll wait followed by a blocking epoll wait for each serial request.

Registration and completion polls go to the env outside the timed workloads.
All requests in the workload table go to the forge. The process driver and the
measurement harness each start the forge once; no workload operation starts a
process. Wire observations used a diagnostic subscriber; speed comparisons and
the final syscall counts omit that subscriber.

## Results and retained changes

The original native grade pair scored 193/195 speed points. Its workload medians
were 394.277/387.629 ms push, 85.485/71.711 ms clone, 512.484/504.512 ms read,
and 103.089/107.949 ms write. Both passed 120/120 and replay matched.

Two uninstrumented runsc baselines scored 132/139:

| Run | Push ms | Clone ms | Read ms | Write ms |
| --- | ---: | ---: | ---: | ---: |
| Baseline 1 | 1047.341 | 157.352 | 1743.434 | 246.145 |
| Baseline 2 | 760.497 | 183.689 | 1511.321 | 241.361 |

Host noise later became substantial. In a paired baseline / inflater / frame /
frame / inflater / baseline sandbox sequence, totals were 134/134/133/127/129/101.
The last baseline read median alone rose to 3348 ms. Therefore the native
all-200 scores and individual faster sandbox rounds do not establish a large
production improvement. The target of 170 production speed points remains open.

Retained:

- Reuse a synchronous inflater across pack entries, resetting its window each
  time. Its output has one spare byte to detect an exceeded declared size. Adler-32,
  stream endings, pack SHA-1, object SHA-1, delta resolution, size limits, and
  atomic publication after complete pack validation remain enforced. Errors
  close the context; subsequent requests create a fresh one. Node 22 internal
  `writeSync`/`_writeState` fields are guarded; unsupported layouts use
  `inflateSync`. This internal interface is the material compatibility risk.
- Use Node's one-shot SHA-1 API for a complete checksum input. In an isolated
  native 50,000-call, 1 KiB probe, this took about 75 ms versus 103 ms for the
  streaming wrapper. Object IDs retain their original hashing format.
- Omit kernel TCP keepalive probes when accepting sockets. HTTP persistence
  keeps its original 65-second idle timeout; TCP_NODELAY remains enabled.
  This saves one setsockopt per accepted connection.

An isolated alternating pack probe decoded 200 three-object push-fixture packs
per iteration, with ten iterations per implementation. Inflater-only p50s were
8.093 -> 4.641 ms natively and 16.156 -> 7.126 ms in runsc. Both versions used
the same checksum function and exact fixture builder. These are CPU probes,
not scored push timings: HTTP and client work still dominate a full workload.

Native syscall counts include all three timed rounds, surviving process TIDs,
and background runtime work. Small changes in futex/memory counts are noisy.

| Workload | Forge before/op | Forge after/op | Suite before/op | Suite after/op |
| --- | ---: | ---: | ---: | ---: |
| Push | 8.982 | 8.808 | 9.335 | 9.402 |
| Clone | 15.947 | 15.980 | 21.647 | 21.720 |
| Read | 4.072 | 4.068 | 4.434 | 4.350 |
| Write | 4.063 | 4.083 | 4.352 | 4.603 |

Clone includes connection establishment, mostly in round one. Server setsockopt
fell from 1.213 to 0.587/op (two to one per accepted socket); total clone calls
did not fall because polls, background GC and connection counts varied.
Serial read/write still require approximately one read, one writev and two
polls per reply on the forge. A runsc trace restricted to clock syscalls found
none inside timed rounds; its vDSO serviced the hot clock reads.

Discarded after repeat grades and sandbox comparisons: contiguous Latin-1
responses; cached complete header/body frames; pre-encoded issue bodies with
different header lookup; disabling HTTP idle timers; leaner headers; fewer V8
background threads and larger nursery; custom net/read/write experiments;
and active polling. None established a repeatable sandbox workload gain.
Every measured full-suite variant passed all 120 checks with the same state hash.

New pack tests cover a valid outer checksum with a corrupt or truncated zlib
stream, undersized/oversized output declarations, dictionary requirements,
failure isolation, recovery, empty objects, multiple compression levels and
outputs larger than 64 KiB. No InstaBox machine was created.

## Reproduce

Run from this checkout with an absolute KIT path. OUTPUT must be a writable
directory outside the read-only kit. The development harness uses the unmodified
SDK conformance runner and benchmark; official grade-local remains the replay
and scoring check. It never runs as part of the entry.

```sh
REPO="$PWD"
KIT=/workspace/projects/2de6bbf5-9a6e-4c92-adbd-7caaf9109ad3/makefun/github-core-clone-01-kit
OUT=/tmp/forge-speed/repeat
RUNSC=/tmp/forge-speed/bin/runsc
STRACE=/tmp/forge-speed/strace/usr/bin/strace

# Native CPU profile: profiling adds overhead; do not score these times.
taskset -c 0,1 node scripts/measure-speed.mjs "$KIT" "$OUT/profile" \
  --cpu-prof --cpu-prof-dir="$OUT/profile" --cpu-prof-name=forge.cpuprofile

# Native syscall accounting, split by workload and process.
taskset -c 0,1 "$STRACE" -f -ttt -qq -s 0 -o "$OUT.trace" \
  node scripts/measure-speed.mjs "$KIT" "$OUT/trace"
node scripts/count-syscalls.mjs "$OUT/trace" "$OUT.trace"

# Run both env and forge in the same rootless sandbox/netstack on two CPUs.
# Use only trusted local code with force-overlay=false; output stays under /tmp.
taskset -c 0,1 "$RUNSC" --rootless --root=/tmp/forge-speed/runsc-root \
  --platform=systrap --network=none do -quiet -force-overlay=false \
  /usr/bin/node "$REPO/scripts/measure-speed.mjs" "$KIT" "$OUT/gvisor"

# Official worktree grade and replay, twice for determinism.
(cd "$KIT" && taskset -c 0,1 node cli/makefun.mjs grade-local \
  --kit . --bot "$REPO" --driver process --worktree)
```

The local runsc installation is the official ARM64 release tar with verified
SHA-512, including its adjacent sidecar binaries. Native Node is v22.23.5.
Before interpreting a small improvement as production points, repeat an A/B
comparison on a quiet host matching the production architecture and container
quotas. A useful next experiment is an HTTP responder latency floor measured
through the same env client and netstack.
