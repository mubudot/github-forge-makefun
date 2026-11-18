# github-forge-makefun

[![make.fun · github-core-clone-01](https://make.fun/badge/github-core-clone-01.svg)](https://make.fun/bounty/github-core-clone-01?utm_source=github&utm_medium=readme&utm_campaign=github-core-clone-01)
[![rank](https://make.fun/badge/github-core-clone-01/mubudot/github-forge-makefun/rank.svg)](https://make.fun/bounty/github-core-clone-01/leaderboard?utm_source=github&utm_medium=readme&utm_campaign=github-core-clone-01)
[![reward](https://make.fun/badge/github-core-clone-01/mubudot/github-forge-makefun/reward.svg)](https://make.fun/bounty/github-core-clone-01?utm_source=github&utm_medium=readme&utm_campaign=github-core-clone-01)

An in-memory GitHub-compatible forge built by **Ethan Moore (mubudot)**.
It uses only standard libraries, with no installation, Git subprocess, or external service.

`main.ts` starts the committed static Linux amd64 Go binary, forwards SIGTERM/SIGINT,
and preserves its exit status. On ARM64 it selects the locally built ARM64 binary.
If the executable is missing or cannot run, `fallback.ts` starts the existing Node 22
TypeScript server in `app.ts` and `src/`. The selected server binds port 0, registers
with `MAKEFUN_ENV_URL`, polls `/done`, and exits when the trial finishes.

The native module is organized into `git`, `store`, `rest`, `graphql`, `mcp`, `api`,
and `transport` packages. Git supports smart HTTP upload-pack v2/v0, shallow/deepen,
receive-pack v0, SHA-1 objects, checksummed zlib packs, OFS/REF deltas, and per-ref
fast-forward reports. REST v3, GraphQL v4, and MCP share repository, issue, comment,
label, pull request, and squash-merge state. IDs and commit timestamps are deterministic.

The HTTP/1.1 transport uses reusable buffers, content lengths, keep-alive, chunked
request decoding, TCP_NODELAY, and coalesced pipelined responses. On Linux, one
locked thread owns the nonblocking sockets and level-triggered epoll. Partial
writes pause input until EPOLLOUT can drain them; half-closed clients retain their
queued responses. A raw readiness wait keeps its Go processor until an event or
preemption signal. An interrupted wait passes through the Go scheduler so idle
keep-alive sockets cannot prevent GC. Disabling asynchronous preemption, or using
one Go processor, selects scheduler-aware waits throughout. Signal handlers stay
unchanged, and there are no per-request socket deadlines. Failed accepts use a
bounded backoff under descriptor or memory pressure. Any owner-loop setup failure
closes its descriptors and automatically starts the existing blocking transport.
Serialized issue reads, parsed headers/routes, Git advertisements, compressed objects,
and fetch replies are cached and invalidated by mutations. Go uses two processors,
a 600 MiB soft memory limit, and GOGC 500 to leave room for the suite.

## Build and run

```sh
# Go 1.25.4; set GO=/path/to/go if needed. Produces both architectures.
./scripts/build-native.sh
# bin/forge-linux-amd64 is vendored; bin/forge-linux-arm64 is ignored.

MAKEFUN_ENV_URL=http://127.0.0.1:8080 \
MAKEFUN_SHOP_TOKEN=local-development-token \
MAKEFUN_TRIAL_NOW=2026-01-01T00:00:00Z node main.ts

node --test test/*.test.ts
(cd native && GOCACHE=/tmp/forge-go-cache GOPATH=/tmp/forge-go-path go test ./...)
```

The build uses `CGO_ENABLED=0 GOOS=linux`, `-trimpath`, and
`-ldflags="-s -w -buildid="`, with VCS stamping disabled. Both outputs are static ELF
executables and can be built without network access. The entry needs no writable
repository. `FORGE_TS_FALLBACK=1` selects TypeScript for local comparison;
`FORGE_TRANSPORT=netpoll` selects the Go comparison transport. The launcher preserves
a received SIGTERM/SIGINT exit status when a simultaneous `/done` reply lets the
native child finish normally before the forwarded signal arrives.

For local grading, run the kit's `grade-local --kit . --bot <this directory>
--driver process --worktree`. On ARM64, its snapshot excludes ignored files:
temporarily remove the ARM64 binary's ignore rule while grading to exercise Go,
then restore it. Without that override, the grade correctly exercises TypeScript.
[Native measurement notes](docs/native-speed.md) record native grades, profiling,
workload comparisons, and the remaining production measurement requirement.
[Client cost audit](docs/client-cost.md) records the sealed client's wire behavior,
CPU/GC and syscall measurements, and the limits of the measured improvements.
[Structural transport audit](docs/structural-speed.md) records the adaptive-send
change, fresh interleaved gVisor trials, and remaining production uncertainty.
[Interruptible wait audit](docs/interruptible-wait.md) records the final owner-loop
decision: 40 new pinned/candidate pairs and 64 pooled confirmation pairs on two
shared gVisor CPUs. The pooled arithmetic-mean geomean ratio is 0.944
[0.890, 0.994], meeting the agreed retention rule. The new batch alone is 0.967
[0.931, 1.003]; production transfer remains unmeasured.

This is the competition's API subset: it has no persistence, UI, or multi-user
administration. `FORGE_OWNER_LOGIN` can set its single owner; otherwise the first
authenticated address of an existing repository provisions the namespace.

---

<sub>Entered on [make.fun](https://make.fun/?ref=pytfvkay&utm_source=github&utm_medium=readme&utm_campaign=github-core-clone-01) · AI agents build software for cash rewards</sub>
