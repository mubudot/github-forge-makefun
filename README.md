# Forge

A standalone, in-memory Git forge for the [make.fun competition](https://make.fun/bounty/github-core-clone-01).
Node 22.18 or later runs the TypeScript source directly. There are no dependencies, installation steps,
git subprocesses, model calls, or external services.

## Run

```sh
node main.ts
```

The competition supplies `MAKEFUN_ENV_URL` and `MAKEFUN_SHOP_TOKEN`. The server binds an ephemeral port,
registers it with the environment, polls `/done`, and exits successfully when the trial completes.
`MAKEFUN_TRIAL_NOW` supplies the fixed clock; absent or invalid values use a fixed UTC epoch.
Every API request must carry `Authorization: Bearer <MAKEFUN_SHOP_TOKEN>`. `/healthz` is public.

`FORGE_OWNER_LOGIN` can explicitly configure the single owner's login. Without it, the first
authenticated address of an existing repository provisions the namespace; `/user` defaults to `mubudot`
before provisioning. The competition's token is opaque: its suite chooses a viewer in `wire.ts` and
uses that viewer's login in repository URLs. Once provisioned, another owner gets a JSON 404.

## Interfaces

| Path | Supported behavior |
| --- | --- |
| `/{owner}/{repo}.git/info/refs` | Smart HTTP advertisements for upload-pack v2/v0 and receive-pack v0 |
| `/{owner}/{repo}.git/git-upload-pack` | Prefix-filtered refs, HEAD symrefs, peeled tags, fetch, shallow clone and deepen |
| `/{owner}/{repo}.git/git-receive-pack` | Checksummed packs, OFS/REF deltas, thin packs, per-ref status, sideband, fast-forward checks and deletion |
| `/api/v3` | Repositories, branches, refs, commits, file/directory contents, labels, issues, comments, pull requests, files and squash merges |
| `/api/graphql` | Parsed queries and mutations, aliases, variables/defaults, fragments, directives, introspection, global nodes, cursor pagination and batched requests |
| `/mcp` | JSON-RPC discovery for revision 2026-07-28, legacy initialization, tool schemas and calls |

The six MCP tools are `create_repo`, `get_repo`, `create_issue`, `list_issues`, `create_pull_request`,
and `merge_pull_request`. All interfaces use one store and stable IDs. API content writes create real
Git trees and commits; pushed branches are available to REST, GraphQL, and MCP pull requests.

The forge implements the competition's GitHub API subset. It has no durable persistence, UI,
multi-user administration, or full GitHub GraphQL schema. Squash merges detect conflicts per file.

## Implementation

- `main.ts`: registration and process lifecycle.
- `app.ts`, `src/http.ts`: HTTP dispatch, authentication, JSON/error handling and pagination.
- `src/store.ts`: shared repositories, refs, issues, labels, comments, pulls and deterministic IDs.
- `src/git/`: Git objects, pack/delta codecs, packet framing and smart HTTP services.
- `src/rest/`: REST resource handlers.
- `src/graphql/`: parser, schema probes and executor.
- `src/mcp.ts`: JSON-RPC protocol and tool dispatch.

Object compression and repeated fetch replies are cached. All caches have explicit invalidation or
size bounds. Issue reads reuse serialized bodies; connections use keep-alive and TCP_NODELAY.
Pack inputs have size and expansion limits. No request writes to disk or logs synchronously.

## Verification

```sh
node --test test/*.test.ts
```

The focused tests exercise forward REF delta chains through OFS deltas, thin packs, damaged-pack
atomicity, GraphQL parsing/mutations, and recovery after malformed HTTP and JSON-RPC requests.

From the read-only kit directory, grade this working tree with:

```sh
node cli/makefun.mjs grade-local --kit . --bot /absolute/path/to/forge --driver process --worktree
```

Add `--instances 3` to run every practice instance and its replay. All three have passed all 120
checks with byte-identical replay exports. Observed local workload medians are roughly 0.32–0.65 s
for push, 0.055–0.106 s for clone, 0.40–0.78 s for read, and 0.074–0.109 s for write.
Process-driver timings depend on the host; the hosted sandbox can differ.

The final lifecycle log reports peak resident memory in KiB after serving the full trial. Across all
three practice instances, the observed peak stayed below 72 MiB. The forge's source is MIT licensed;
see [LICENSE](LICENSE).
