**KEEP the hardened owner loop under the agreed decision rule.** The worktree
entry enables it, and the tracked amd64 executable has been rebuilt. No repository
commit, push, or submission was made. Production performance remains unmeasured.

The final batch contains **40 fresh pinned/candidate pairs**, alternating AB/BA,
with the unchanged sealed suite, five warmups and exactly three timed rounds.
Both processes share two CPUs / 1,280 MiB in runsc-netstack `release-20261116.0`;
Node 22.23.4 uses old-space=256 MiB and semi-space=1 MiB. No tracing, profiling,
compilation or other benchmark ran alongside the rig trials. Every one of the
80 new trials passed 120/120, including 15/15 MCP checks, and exported the required
state hash. The pinned binary is exactly the `d0a2974` Git blob, SHA-256
`b0edca87274ff6bfa52038841cc188b11fb1c0fa7eeccac1d853d3681fce8c40`.

The plan fixed 40 pairs before timing and made the ratio of **arithmetic mean
trial perf_p50_us** the primary decision statistic. Intervals are percentile 95%
block bootstraps over whole paired fresh-trial blocks, 50,000 resamples, seed
20261118. Pooling uses only these 40 pairs and the prior **24 fixed confirmation
blocks**, selecting their pinned/owner observations. Discovery and HEAD
observations are excluded. The hardened binary differs from the old prototype;
the connection parser, framing, handlers and ordinary one-read path are retained,
while the changes below fix exceptional paths and split the code into modules.

| Candidate/pinned | Pairs | Mean trial G ms, pinned -> candidate | Arithmetic-mean ratio [95% interval] | Paired-geometric ratio [95% interval] |
| --- | ---: | ---: | --- | --- |
| New batch alone | 40 | 150.066 -> 145.061 | 0.966644 [0.930736, 1.003456] | 0.965311 [0.930341, 1.001363] |
| Pooled confirmation | 64 | 174.546 -> 164.797 | 0.944146 [0.889931, 0.994227] | 0.954443 [0.914268, 0.992755] |

Workload ratios are paired geometric means of each trial's three-round p50.
Each interval resamples the same whole paired blocks as the geomean analysis.

| Workload | New ratio [95% interval] | Pooled ratio [95% interval] |
| --- | --- | --- |
| Push | 0.970850 [0.941884, 0.999713] | 0.958348 [0.919120, 0.996462] |
| Clone | 0.925692 [0.876831, 0.978654] | 0.912086 [0.868895, 0.956465] |
| Read | 0.932546 [0.867758, 1.003096] | 0.940863 [0.881179, 1.002522] |
| Write | 1.036048 [0.978487, 1.097428] | 1.009055 [0.950640, 1.067819] |

All four prescribed conditions pass using unrounded values: pooled primary
upper bound **0.994227 < 1.00**, pooled point **0.944146 <= 0.95**, new-batch
point **0.966644 <= 0.97**, and the largest pooled workload point
**1.009055 <= 1.03**. The new write point regressed 3.6%; the rule's workload
condition concerns pooled points, where write regressed 0.9%. Neither new-batch
geomean interval excludes no gain. This is a retention decision under that rule,
not a claim that an 8% production reduction was established.

Sensitivity checks preserve short-range ordering: pooled arithmetic-mean
intervals are **[0.889260, 0.995637]** with adjacent two-pair blocks and
**[0.889671, 0.995667]** with adjacent four-pair blocks. Resampling pairs within
each batch gives **[0.890079, 0.994028]**. All these pooled upper bounds remain
below one. These are descriptive intervals from one host; separate member
quotas and production architecture/scheduling variation remain limitations.

Hardening was completed before the build was frozen:

- Backpressured connections subscribe only to EPOLLOUT. A peer's persistent
  RDHUP cannot repeatedly wake a socket whose output is still blocked.
- HUP no longer discards unread buffers after one successful read. Input is
  drained through actual EOF, including large bodies and pipelined requests;
  closed-side EAGAIN and fatal output/HUP combinations terminate without spinning.
- A nonblocking Unix socketpair wakes shutdown. Failed writes close its peer,
  which wakes epoll through EOF/HUP; concurrent/idempotent stop waits for complete
  descriptor cleanup, and finished maps release their buffers.
- Every one of the eight listener/control setup failure stages closes all
  acquired descriptors before automatically starting `ServeBlocking`.
- Descriptor or memory exhaustion removes the listener from epoll and retries
  after a close or a 100 ms backoff. Established connections continue. The
  gVisor stress trace also shows that an EMFILE accept can reset a queued socket
  before returning any accepted fd; the test explicitly distinguishes this
  kernel behavior and verifies recovery with a fresh connection.
- EINTR is retried for control operations. Waits pass through Go's scheduler
  after preemption, and raw waiting is disabled with one Go processor or disabled
  asynchronous preemption. Signal dispositions stay unchanged.
- The Node launcher records the first received SIGTERM/SIGINT and preserves its
  status when a simultaneous `/done` reply lets the child exit normally first.

`owner_linux.go` owns setup, lifecycle and events; `owner_conn_linux.go` owns
buffered HTTP state; `owner_syscall_linux.go` contains the bounded raw operations
and interruptible wait. Tests cover setup fault injection, real fallback, a
32 MiB fragmented request with 100 Continue, half-close under actual socket
backpressure, a 400-request half-closed pipeline, truncated-body EOF, 100 clients
and 500 exchanges, fd-pressure recovery, descriptor counts, idle/backpressured
shutdown, failed wakeup, and repeated GC. The gVisor transport suite passed three
full repetitions, plus the preemption-disabled run and both signal tests.

Go tests, transport race tests, go vet and all 16 Node tests pass. Two builds
are literally byte-identical for both architectures, and the amd64 bytes match
all 40 candidate trials. The final binary SHA-256 values are:

```
amd64  6f9e9ee0db53dab02503db27a5244a6050a2a7fce190b764e88bda6bdf45fd89
arm64  6f20de6689528b421c0fbbc8a0c1cb6401294da6cb1b556d66a85037b62ffa15
```

Official verification completed twice from the kit directory, each with three
instances. All six live trials passed **120/120**, with **feature 600, agent 200**;
all six replays matched, and both L1 regrades reproduced byte for byte. Every
live/replay state SHA-256 was
`a0e98d49854b961230227867235c8615858e006db3763f341ac6154af1445590`.
The commands used `--driver process --worktree --instances 3` with explicit
`--save /tmp/forge-owner-final-20261118/grade-{1,2}` locations, avoiding the CLI's
default `~/.makefun/trials`. The ARM64 ignore rule was temporarily removed for
these local grades to include the native binary, then restored byte for byte.
The source hashes still match the frozen candidate, and the final amd64 binary
still matches the measured bytes. No kit file was edited.

InstaBox `main` was suspended after the rig completed. Actual and desired states
were both verified **suspended** at **2026-11-18T10:54:00.582719Z**; no benchmark
container remained running before suspension.

[Compact results](interruptible-wait-results.json) retain all **236 speed trials**,
the predeclared plan, complete new and pooled analyses, bootstrap source,
frozen candidate patch and verification records. New artifacts are under
`/tmp/forge-owner-final-20261118` and rig `/data/home/fg/owner-final-20261118`.

**Earlier exploration, before the final decision.** At that stage, the entry
remained at `2cff491`. Interruptible reads removed most scheduling calls but failed fresh
confirmation. A single-owner transport reduced forge CPU by 29.8%, yet its
confirmed mean geomean improved only 5.7% over HEAD. Neither experiment establishes
the requested >=8% production reduction.

[Measurements](interruptible-wait-results.json) preserve all **156 fresh sealed
trials**, CPU windows, workload medians, binary hashes, syscall diagnostics and
comparisons. The supplied production observations, 374.3 / 420.8 / 457.7 ms for
`d0a2974` / `2cff491` / `ca345f7`, remain unpaired context; no new production
trial or submission was made.

All speed trials used the designated amd64 InstaBox machine, runsc-netstack
`release-20261116.0`, and Node 22.23.4. Forge and client shared **two CPUs and
1,280 MiB** in one sandbox. This matches the requested joint CPU constraint,
but does not impose separate member memory/CPU quotas. Node used old-space=256
MiB and semi-space=1 MiB; maximum client RSS was **185.42 MiB**. Desktop processes
were paused during the speed batches. No profiling, tracing, or compilation ran
alongside them on the rig.

The original sealed env, fixtures, 120 checks, five warmups and exactly three
timed rounds were preserved. Counters bracket the existing timers. Forge CPU is
the process-wide `/proc/PID/stat` user+system delta, including its threads;
client CPU is `process.cpuUsage()`, including background threads. Both counters
are quantized to 10 ms in this rig. Local and rig sealed-client and calibration
hashes match. Serial workloads use two persistent sockets; clone reaches 100.

The following table uses all 72 timed windows per workload from the **24 HEAD
trials in the final fixed batch**, rather than the initial three-trial screen.
Values are arithmetic mean **microseconds per HTTP request**. Push has 400,
clone 250, read 2,000 and write 200 HTTP requests per timed round.

| Workload | Forge CPU us/request | Client CPU us/request | Wall us/request |
| --- | ---: | ---: | ---: |
| Push | 321 | 727 | 708 |
| Clone | 151 | 558 | 486 |
| Read | 129 | 293 | 283 |
| Write | 252 | 551 | 535 |

Clone wall is amortized throughput time across concurrent requests, not an
individual RTT. CPU overlaps on two cores; these counters cannot be subtracted
from wall to isolate netstack delay. The absolute CPU costs differ from earlier
four-CPU measurements and are not production estimates.

The initial read/wakeup target had enough potential to justify a cheap proof.
Two finite-wait variants used a two-millisecond socket timeout to bound raw reads,
then a scheduler-aware poll for idle connections. Enabling timeouts lazily looked
promising in four pairs but produced 0.7–1.5 s first write rounds. Neither variant
was retained. No root cause for those stalls is asserted.

The next variant used raw reads only after four short connection-activity gaps.
It preserved Go's SIGURG handler and mask while clearing SA_RESTART. A preemption
signal therefore interrupted an idle read; EINTR switched that read to an ordinary
Go syscall, allowing GC/STW. Asynchronous-preemption-off configurations retained
ordinary reads. Tests exercised 1,000 forced GC cycles with two idle raw-read
threads in gVisor. This was a successful scheduling proof, not a successful speed
change: its four-pair discovery ratio of 0.842 failed twelve fixed confirmation
blocks against both baselines. The
[rejected read patch](interruptible-read-rejected.patch) records the prototype.

The largest CPU opportunity remaining in the table was to remove connection
thread fan-out as well as serial handoffs. The owner-loop prototype uses one
locked thread, nonblocking accepted sockets, one payload read per readiness
event, coalesced writes and level-triggered epoll. EPOLLOUT resumes partial writes;
backpressure pauses input. Generation tags reject events for reused descriptors.
Its raw epoll wait retains the Go processor until readiness or SIGURG. EINTR
switches the next wait through Go's scheduler-aware wrapper. With asynchronous
preemption disabled, every wait uses that wrapper. There are no raw blocking
socket reads, per-read deadlines, or changed signal dispositions.

Linux multiplexing waits return EINTR even with SA_RESTART, unlike ordinary
blocking reads. This is the relevant distinction in the
[Linux signal documentation](https://man7.org/linux/man-pages/man7/signal.7.html).
The owner prototype passed 500 forced GC cycles with twenty idle sockets in
gVisor, large pipelined backpressure tests, Go tests/vet/race tests, and all 16
Node tests. One initial SIGINT fixture race also appears in the prior adaptive
logs; the full rerun passed, with the launcher unchanged. Both architectures
built reproducibly. The [rejected owner patch](interruptible-owner-rejected.patch)
preserves this implementation without enabling it in the entry.

Three-variant batches rotated all six orders of HEAD, pinned and candidate.
Each block contained fresh containers and clients for every variant. The fixed
confirmation batches were completed in full. The primary statistic is the
candidate/base ratio of arithmetic mean trial `perf_p50_us`, matching the final's
mean of fresh-trial geomeans. Intervals resample whole blocks 20,000 times, seed
20261118, and remain conditional on this host and these observations.

| Fixed confirmation | Blocks | Mean G ms, HEAD / pinned / candidate | Candidate/HEAD [95% interval] | Candidate/pinned [95% interval] |
| --- | ---: | --- | --- | --- |
| Interruptible read | 12 | 251.30 / 239.04 / 261.83 | 1.042 [0.903, 1.201] | 1.095 [0.949, 1.247] |
| Single owner | 24 | 209.70 / 215.35 / 197.69 | 0.943 [0.873, 1.016] | 0.918 [0.815, 1.018] |

The owner loop's six-block discovery ratios were 0.873 against HEAD and 0.878
against pinned. Fixed confirmation was weaker. Its 8.2% point estimate against
pinned alone does not establish the required transferable reduction. The
paired-geometric ratios in confirmation were 0.949 against HEAD and 0.937 against
pinned; changing the aggregation does not establish the target either.

| Workload, owner confirmation | Mean forge CPU ms/round, HEAD -> owner | Paired p50 wall ratio |
| --- | ---: | ---: |
| Push | 128.5 -> 101.1 | 0.978 |
| Clone | 37.6 -> 20.0 | 0.885 |
| Read | 257.8 -> 177.4 | 1.019 |
| Write | 50.4 -> 34.7 | 0.922 |

Forge CPU fell 29.8% overall, client CPU 4.5%. Clone used six forge threads
instead of about 106. Read CPU fell 31.2% without an improved paired read p50.
These results are consistent with substantial saved work being overlapped with
client processing; they do not measure a precise critical-path or netstack split.

Separate **in-gVisor amd64** strace runs confirm that both experiments removed
calls and wakes. These timings are excluded from every speed comparison. Counts
include all forge threads, occasional lifecycle activity and boundary-spanning
calls; tracing perturbs scheduling. Positive futex WAKE returns count tasks
woken. Socket-name resolution was unavailable in these traces, so the retained
classification is by forge process/thread and syscall, not socket inode.

| Workload | HEAD calls/request | Raw-read calls/request | Owner calls/request | HEAD wakes/request | Raw-read wakes/request | Owner wakes/request |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| Push | 6.847 | 2.709 | 3.454 | 0.853 | 0.076 | 0.083 |
| Clone | 5.796 | 5.791 | 2.683 | 0.652 | 0.684 | 0.036 |
| Read | 5.937 | 2.325 | 3.181 | 0.893 | 0.036 | 0.034 |
| Write | 7.543 | 2.645 | 3.533 | 1.142 | 0.082 | 0.073 |

The read prototype removed about 96% of read futex wakes and 61% of its syscall
entries, yet failed the wall comparison. Call count alone is therefore an
insufficient retention criterion for this entry.

Every speed trial passed 120/120, including 15/15 MCP checks, with state SHA-256
`a0e98d49854b961230227867235c8615858e006db3763f341ac6154af1445590`.
The candidate amd64 binary was
`f5357051684cdde61be66aaff1bfeea195464ba408c20eabea6ebce1667deb6e`.
The unchanged `2cff491` amd64 used in that audit was
`38c91087fbc534c197c2ed56dc7c59d0e3706416efb6db20db90e2fc73d66ff2`.
No runtime source, default, executable, or kit file was changed during that earlier audit.

The portable development probe now records both processes' CPU and wall windows:

```sh
node --max-old-space-size=256 --max-semi-space-size=1 \
  scripts/measure-process-cost.mjs ../github-core-clone-01-kit \
  /tmp/forge-cost bin/forge-linux-arm64
```

Use the executable matching the host architecture. On the amd64 gVisor rig,
mount the checkout and kit read-only, a writable output directory separately,
and run the same probe with `bin/forge-linux-amd64` in the pinned Node image,
joint `--cpus 2 --memory 1280m`, and the V8 flags above. It imports the sealed
bundle without modifying kit files, and writes `cost.json`, state and logs.
Its own local and gVisor validations passed 120/120 with the same state hash.

Artifacts remain in `/tmp/forge-transport-20261118` and rig
`/data/home/fg/transport-20261118`. The result JSON embeds the original timing
harness and batch runner for reproduction. The archived experiment patches apply
to `2cff491`; apply each separately in a temporary copy, then build with
`scripts/build-native.sh`. Both were rejected at that earlier stage. The final
worktree now enables the hardened owner implementation described above; its
source patch and hashes are recorded separately in the final results. No commit,
push, or submission was made.

That earlier audit suspended InstaBox `main`; actual and desired states were both verified
`suspended` at 2026-11-18T09:59:46.492311Z.
