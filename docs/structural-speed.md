# Structural transport audit, 2026-11-18

Retain adaptive small-response sending against HEAD `93602c9`. In **32 fresh
interleaved gVisor pairs**, mean trial geomean fell **294.041 → 270.798 ms**:
ratio **0.921**, paired 95% interval **0.860–0.981**. Production remains unmeasured.
[Results](structural-speed-results.json) retain all 112 full-suite trials,
including rejected experiments, raw rounds, syscall counts, and verification.

## Scoring and request costs

The sealed client's `bench.ts` / `manifest.ts` award
`round(clamp(25 - 12.5*log2(T/T_ref), 0, 50))` per workload. Each workload has
one quarter of the tie-break's log weight: `G=(push*clone*read*write)^(1/4)`.
Each T is its three-round median; final trials' tie-breaks are averaged.

| Workload | Production T_ref ms | Largest ms earning rounded 50 | Timed HTTP requests/round |
| --- | ---: | ---: | ---: |
| Push | 1640.820 | 421.737 | 400: 200 serial advertisement/receive-pack pairs |
| Clone | 266.941 | 68.611 | 250: 50 concurrent clones, five exchanges each |
| Read | 2759.715 | 709.325 | 2,000 serial issue GETs |
| Write | 348.965 | 89.694 | 200 serial issue POSTs |

The supplied production aggregate, 156 points / 374.3 ms, cannot identify its
individual workload times or points. The reference geomean is 805.899 ms.
Reaching 322 ms requires a 14.0% geomean reduction, or 45.2% in one workload alone.
In this rig's baseline, clone accounts for 13.75 of the 27.75 missing points.

Forge-only ARM64 strace entries per **HTTP request**, across the twelve timed
windows (diagnostic counts, never speed timings):

| Workload | HTTP/op | All calls/request | Socket reads/request | Writes/request | Futex tasks woken/request |
| --- | ---: | ---: | ---: | ---: | ---: |
| Push | 2 | 7.557 | 1.001 | 1.000 | 0.881 |
| Clone | 5 | 5.219 | 1.093 | 1.000 | 0.636 |
| Read | 1 | 5.704 | 1.000 | 1.000 | 0.741 |
| Write | 1 | 8.817 | 0.998 | 1.000 | 0.990 |

Clone includes 71 EOF reads and 70 new connection threads, concentrated in round
one; a whole-tree trace opened 97. Boundary-spanning reads and tracing perturb
these counts. Wake counts are positive futex WAKE returns, not all scheduling
activity. There were no workload disk writes or subprocesses. An 8 KiB buffer
already receives ordinary requests in one payload read.

The whole-tree trace confirms Go PID 16194 owns the registered listener; Node
supervisor PID 16163 sends no workload TCP exchanges. Go exec precedes the first
timed round by 2.259 s. Registration, health checks, conformance, fixtures and
warmups precede timing. Node selects/spawns the core and forwards signals.

## Ranked changes

1. **Adaptive send, retained:** after four gaps of at most four global writes,
   a connection sends replies up to 4 KiB with
   `RawSyscall6(sendto, MSG_DONTWAIT|MSG_NOSIGNAL)`, avoiding a Go processor handoff.
   Longer gaps and larger replies use blocking writes; EAGAIN/EINTR fall back to
   blocking writes, preserving partial-write retries. No route or fixture is
   recognized. All native serial replies used sendto; 97.6% of clone replies
   kept blocking writes. Read futex wakes fell 4448 → 3080; clone rose 477 → 535.
2. **Prepared frames and immutable read snapshots, rejected:** cached Git frames
   and REST framing with fresh IDs bypassed the store mutex on cached reads.
   Local issue dispatch changed 80 → 95–97 ns; wall improvement was unconfirmed.
3. **Immediate processor release before blocking reads, rejected:** clone futex
   wakes rose 477 → 1575, and wall time regressed despite reduced forge CPU.

Bodies, parsed headers, keep-alive, contiguous replies and synchronous connection
handlers were already optimized. There is no per-request application goroutine
or channel handoff. Push validates three new objects and changes its advertisement
per operation; write creates new state/JSON. Prior rejected header/JSON reductions
were not retained. Larger buffers cannot remove the existing one-read minimum.

## Interleaved rig evidence

Each fresh runsc-netstack `release-20261116.0` container runs one amd64 Go core,
Node 22.23.4 sealed client, all 120 checks, unchanged fixtures, five warmups and
exactly three rounds. AB/BA order alternates by pair. Client/calibration hashes
match the kit. No tracing, profiling, compilation or other benchmark runs alongside.

Screens share 2 CPUs / 1280 MiB; adaptive trials share **4 CPUs / 1280 MiB**.
This approximates the production pod aggregate and does **not** enforce separate
entry 2-CPU/1-GiB and env 2-CPU/256-MiB quotas. Node uses old-space=256 MiB,
semi-space=1 MiB; maximum client RSS was 183.32 MiB. An attempted multi-container
setup completed no trial and was excluded/removed. The rig starts Go directly.

| Experiment | Fresh pairs | Shared CPUs | Mean trial G ratio [paired 95% interval] |
| --- | ---: | ---: | --- |
| Prepared read snapshots | 12 | 2 | 0.987 [0.879, 1.126] |
| Blocking-read hint | 12 | 2 | 1.093 [1.012, 1.176] |
| Adaptive discovery | 16 | 4 | 0.942 [0.846, 1.035] |
| Adaptive fixed confirmation | 16 | 4 | 0.901 [0.829, 0.976] |
| Adaptive pooled | 32 | 4 | **0.921 [0.860, 0.981]** |

Workload ratios are geometric means of paired p50 ratios; points and CPU are means
across the same 32 pairs. CPU comes from forge `/proc/PID/stat`, quantized to 10 ms.

| Workload | Wall ratio [95% interval] | Points base → candidate | Forge CPU ms/round base → candidate |
| --- | --- | ---: | ---: |
| Push | 0.939 [0.890, 0.990] | 48.44 → 48.91 | 282.1 → 263.6 |
| Clone | 0.978 [0.903, 1.058] | 36.25 → 36.59 | 87.9 → 83.5 |
| Read | 0.951 [0.859, 1.046] | 45.66 → 46.84 | 686.3 → 592.4 |
| Write | 0.891 [0.809, 0.983] | 41.91 → 43.91 | 109.3 → 96.1 |

Paired geometric-mean trial ratio: **0.939 [0.887, 0.994]**. The primary arithmetic
mean above targets the final's mean of fresh trial medians. Intervals resample
whole pairs 20,000 times (seed 20261118). Batch-stratified two/four-pair blocks
give mean-ratio intervals 0.862–0.974 / 0.876–0.972. Forge CPU fell 11.1% overall.
Host drift, candidate selection and resource approximation limit production
transfer; no production score or 322 ms result is claimed.

## Verification

Go tests, `go vet`, and all 16 Node tests pass. The new small-write backpressure
test preserves exact bytes; its separate trace exercises two EAGAIN fallbacks.
Two `scripts/build-native.sh` builds are byte-identical for both architectures.
The final amd64 binary matches every adaptive rig trial:
`38c91087fbc534c197c2ed56dc7c59d0e3706416efb6db20db90e2fc73d66ff2`.

From the kit, `grade-local --kit . --bot <repo> --driver process --worktree
--instances 3 --save /tmp/forge-structural/process-grade-adaptive` passes 120/120,
feature 600, agent 200 on all three instances; replays match and L1 regrade is
byte-identical. `--worktree` tests uncommitted files. Temporarily unignoring the
ARM64 binary exercises Go on this host; the original ignore rule is restored.
All live state hashes:
`a0e98d49854b961230227867235c8615858e006db3763f341ac6154af1445590`.

Artifacts remain in `/tmp/forge-structural` and rig `/data/home/fg/structural`.
No commit, push or submission was made. InstaBox `main` was suspended; actual and
desired states were verified suspended at 2026-11-18 08:03:57 UTC.
