# Forge CPU and wakeup measurements

The implementation stays at **d0a2974**. None of the tested changes established
a repeatable improvement of at least 4% in the geomean of the four workload
walls. The shared epoll prototype reduced forge CPU consistently, but its wall
improvement did not survive confirmation. Only these notes and their
[measurements](runtime-cost-results.json) are retained.

## Measurement

All comparisons ran on the designated InstaBox machine with `runsc-netstack`.
Node and Go shared a sandbox with a joint two-CPU quota and 1,280 MiB memory
limit. Node used `--max-old-space-size=256 --max-semi-space-size=1`; fresh full
suite trials peaked around 170–180 MiB client RSS. This approximates competition
within one sandbox, but does not reproduce production's separate member quotas.

The probe imported the kit's sealed `program/env/server.mjs`. Discovery runs
yielded between its existing warmup and timed-round boundaries; they alternated
AB/BA rounds against two Go processes. Confirmation trials used a fresh
container, one Go process, and the entire sealed suite, including conformance,
original fixtures, five warmups, and exactly three rounds. Variant order was
alternated or rotated. All full-suite trials passed 120/120 and exported the
required state hash.

CPU is the difference in the forge process's `/proc/PID/stat` utime + stime,
which includes its threads, at each timed boundary. The rig exposes 100 ticks/s,
so each window is quantized to 10 ms; sums and means are preferable to individual
short clone/write readings. Discovery's Go `getrusage` readings agreed with
these counters. Optional runtime-stat RPCs happened outside the windows and
were disabled for fresh confirmations. Profiles and strace runs were separate
diagnostics and are excluded from the wall comparisons.

Workload order throughout the tables is **push / clone / read / write**, with
**200 / 50 / 2,000 / 200** operations per round. Each workload has equal log
weight: `G = (push * clone * read * write)^(1/4)`. For fresh full-suite pairs,
the ratio is the geometric mean of paired candidate/base trial G values, each
computed from the original three-round workload medians. Lower is better.

## Runtime knobs

Each discovery comparison has nine AB/BA round pairs per workload, across three
fixture passes. CPU is the arithmetic mean ms per round. Wall ratios use the
median matched-round ratio for each workload. These longer discovery contexts
retain more prebuilt pushes than the production suite, so the fresh suite
confirmations carry more weight.

| Candidate | Base forge CPU ms P/C/R/W | Candidate forge CPU ms P/C/R/W | Wall ratios P/C/R/W | Wall geomean ratio |
| --- | --- | --- | --- | ---: |
| GOMAXPROCS=1 | 103/39/221/52 | 107/33/211/49 | 1.147/0.996/1.014/0.980 | 1.032 |
| Netpoll | 89/29/217/39 | 66/14/219/33 | 0.955/0.986/1.133/1.255 | 1.076 |
| Blocking without LockOSThread | 104/34/227/40 | 96/28/207/34 | 1.031/0.980/1.049/1.041 | 1.025 |
| GOGC=off, 600 MiB soft limit | 99/33/270/26 | 110/41/270/37 | 1.002/0.958/0.977/1.018 | 0.989 |
| asyncpreemptoff=1 | 108/36/199/31 | 86/42/211/28 | 0.918/1.281/0.985/1.108 | 1.064 |
| madvdontneed=0 | 69/30/201/33 | 67/22/209/30 | 0.904/0.929/0.994/1.008 | 0.958 |
| Two blocking connections, then netpoll | 126/56/306/61 | 128/27/298/51 | 0.882/1.060/0.933/1.043 | 0.977 |
| Four blocking connections, then netpoll | 143/44/399/84 | 126/27/403/70 | 0.966/1.174/1.095/1.241 | 1.114 |

GOMAXPROCS=2 and GOGC=500 remain the defaults. The GOGC=500 screen recorded only
one timed GC across all four workloads; disabling GC had no clear benefit.
Changing scavenger policy produced a promising but unconfirmed wall estimate,
without a clear general CPU reduction. Netpoll with one P and the two-connection
hybrid with one P had wall geomean ratios 1.071 and 1.112 respectively.

Twenty-seven further round pairs did not confirm unlocked threads: CPU changed
81/24/224/40 -> 67/31/222/30 ms, with wall geomean ratio **1.042**. The hybrid's
27-pair ratio was 0.968, but fresh three-round trials exposed clone regressions.
The hybrid reduced threads from about 107 to 9 and clone CPU substantially,
while moving more traffic through the readiness path. It was rejected.

## Shared epoll loop

A 26-second baseline CPU profile contained 5.89 seconds of samples: **79.3%**
were flat in the syscall wrapper, primarily socket reads/writes, and **7.5%**
in futexes. The profile includes setup
and warmups as well as timed work. Handler work accounted for about 10%
cumulatively. This supported investigating transport scheduling rather than
JSON or Git computation.

Separate local forge-only traces found 98 new threads during the first concurrent
clone round on the blocking path. Across three clone rounds, the two-connection
hybrid reduced futex entries from 914 to 240 and new threads from 98 to 1, but
added readiness and socket-conversion calls. Total clone syscall entries changed
32.15 -> 32.99 per operation. Tracing perturbs scheduling; these counts explain
the tradeoff and are not speed measurements. The ephemeral local traces were
lost during the infrastructure restart; the recorded gVisor counters are retained.

A nonblocking raw-send fast path was also rejected: it reduced some serial
wakeups but increased concurrent-clone futex activity and regressed clone walls.
The final prototype instead used one owner for ready sockets, with nonblocking
reads/writes, persistent parser/buffers, bounded queued output, and EPOLLOUT for
backpressure. It retained the HTTP and application behavior. It used about
5–6 threads, compared with about 106–107 after baseline concurrent clones.

| Epoll comparison | Independent fixture/trial pairs | Base mean trial G ms | Epoll mean trial G ms | Paired G ratio |
| --- | ---: | ---: | ---: | ---: |
| AB/BA timed rounds, three fresh containers | 9 round pairs/workload | — | — | 0.994 |
| First full-suite batch | 3 | 145.08 | 146.78 | 1.011 |
| Full-suite confirmation batch | 3 | 216.45 | 178.31 | 0.846 |
| Final prototype against committed baseline | 3 | 148.36 | 148.71 | 1.008 |

The middle batch's large improvement was not reproduced. Pooling these nine
full-suite pairs gives a nominal ratio **0.952**; an exploratory bootstrap over
whole trial pairs spans approximately **0.85–1.05**. This is descriptive, given
host drift and multiple candidate comparisons. It does not establish the
required clear improvement. The final three paired ratios were
**0.899 / 1.105 / 1.032**. The arithmetic means of trial G values in that final
batch were essentially equal, which also fails the intended three-fresh-trial
criterion.

The CPU improvement is more convincing. In the final batch, every full-suite
pair used less timed forge CPU: **1,160 -> 890**, **1,060 -> 770**, and
**990 -> 710 ms**, summed across all twelve benchmark rounds.

| Workload | Mean forge CPU ms/round base -> epoll | Forge CPU ms/op base -> epoll | Median wall ms base -> epoll |
| --- | ---: | ---: | ---: |
| Push | 104.4 -> 90.0 | 0.522 -> 0.450 | 211.64 -> 238.52 |
| Clone | 40.0 -> 14.4 | 0.800 -> 0.289 | 89.88 -> 88.20 |
| Read | 180.0 -> 131.1 | 0.090 -> 0.066 | 318.01 -> 341.41 |
| Write | 32.2 -> 27.8 | 0.161 -> 0.139 | 65.54 -> 72.40 |

CPU fell **26.2%** in this final batch. Lower CPU alone did not translate into
the requested wall improvement, so the event loop and all experimental flags
remain outside the entry. There is no claim of a production speedup.

## Verification and artifacts

The unchanged implementation passed `go test ./...`, `go vet ./...`, and all
16 Node tests. Two runs of `scripts/build-native.sh` reproduced the original
amd64 and ARM64 executables byte for byte. The committed amd64 SHA-256 remains
`b0edca87274ff6bfa52038841cc188b11fb1c0fa7eeccac1d853d3681fce8c40`.

From the kit directory, the official process grade ran with `--worktree`,
temporarily including the ignored ARM64 executable to exercise Go on this host.
The ignore rule was restored. Feature **600**, agent **200**, **120/120** checks,
replay matched, and L1 regrade reproduced byte for byte. State SHA-256:

```
a0e98d49854b961230227867235c8615858e006db3763f341ac6154af1445590
```

The accompanying JSON preserves individual CPU/wall samples, flags, conformance
results, and derived summaries. Development harnesses, profile files, and the
official grade are under `/tmp/forge-runtime-cost`; the rig's corresponding
directory is `/data/home/fg/runtime-cost`. No kit files or Git metadata were
changed. The InstaBox machine is **suspended**.
