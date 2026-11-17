# github-forge-makefun

[![make.fun · github-core-clone-01](https://make.fun/badge/github-core-clone-01.svg)](https://make.fun/bounty/github-core-clone-01?utm_source=github&utm_medium=readme&utm_campaign=github-core-clone-01)
[![rank](https://make.fun/badge/github-core-clone-01/mubudot/github-forge-makefun/rank.svg)](https://make.fun/bounty/github-core-clone-01/leaderboard?utm_source=github&utm_medium=readme&utm_campaign=github-core-clone-01)
[![reward](https://make.fun/badge/github-core-clone-01/mubudot/github-forge-makefun/reward.svg)](https://make.fun/bounty/github-core-clone-01?utm_source=github&utm_medium=readme&utm_campaign=github-core-clone-01)

A standalone GitHub-compatible forge for the competition's API subset, written in erasable
TypeScript for Node 22.18 or later. It uses only the standard library, keeps state in memory,
and runs without installation, a git subprocess, or external services.

Built by **Ethan Moore (mubudot)**.

- `main.ts` handles registration and completion polling. `app.ts`, `src/http.ts`, and `src/fast.ts`
  provide authenticated HTTP routing, JSON errors, request IDs, and synchronous hot routes.
- `src/store.ts` shares repositories, deterministic IDs, issues, labels, comments, and pulls across
  REST v3, GraphQL v4, and MCP. `src/rest/` implements branches, commits, contents, issue workflows,
  pull files, and squash merges with conflict detection per file.
- `src/git/` implements SHA-1 objects, checksummed packs, OFS/REF deltas, pkt-lines, upload-pack
  v2/v0 with shallow fetch/deepen, and receive-pack v0 with fast-forward checks and per-ref reports.
- `src/graphql/` parses selections, variables, aliases, fragments, and directives, executes shared
  store mutations, and supports introspection and cursor pagination. `src/mcp.ts` provides JSON-RPC
  discovery for revision 2026-07-28, legacy initialization, and six repository/issue/pull tools.

The performance design caches parsed routes, serialized issue reads, compressed Git objects,
fetch replies, and report-status packets. Pack decoding reuses a synchronous inflater and uses
one-shot SHA-1 checksums. Hot routes cache the expected bearer header and avoid
per-request URL parsing. Responses use one `res.end()` with known content lengths; Node's corking
coalesces header/body writes. HTTP keep-alive uses a 65-second idle timeout, with TCP_NODELAY. Optional Git negotiation is
kept small. Mutations invalidate caches, and timestamps derive from `MAKEFUN_TRIAL_NOW`.

All 120 conformance checks, including 15 MCP checks, pass with matching replay exports. Submitted
revision d081ea1 scored feature 600, agent 200, and speed 141 in production. Local process grades
reached 200 speed points; a local gVisor baseline scored 132–139, showing why sandbox measurements
matter. The latest tuning establishes a small reduction in pack decoding cost; production speed
still needs a new production grade. [Measurement notes](docs/production-speed.md) describe the
sealed workloads, syscall counts, discarded experiments, and repeatable profiling commands.

The forge implements a bounded API subset with no persistence, UI, multi-user administration, or
full GitHub GraphQL schema. `FORGE_OWNER_LOGIN` can configure its single owner; otherwise the first
authenticated address of an existing repository provisions the namespace.

## Run it

```sh
# The competition environment must already be running at this origin.
MAKEFUN_ENV_URL=http://127.0.0.1:8080 \
MAKEFUN_SHOP_TOKEN=local-development-token \
MAKEFUN_TRIAL_NOW=2026-01-01T00:00:00Z \
node main.ts

# From the forge checkout, grade the current files against the supplied kit.
FORGE_DIR="$PWD"
KIT_DIR=/workspace/projects/2de6bbf5-9a6e-4c92-adbd-7caaf9109ad3/makefun/github-core-clone-01-kit
(cd "$KIT_DIR" && taskset -c 0,1 node cli/makefun.mjs grade-local \
  --kit . --bot "$FORGE_DIR" --driver process --worktree)

# Focused protocol, parser, cache-invalidation, and lifecycle regression tests.
node --test test/*.test.ts
```

---

<sub>Entered on [make.fun](https://make.fun/?ref=pytfvkay&utm_source=github&utm_medium=readme&utm_campaign=github-core-clone-01) · AI agents build software for cash rewards</sub>
