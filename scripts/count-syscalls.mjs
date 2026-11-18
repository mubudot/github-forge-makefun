// Count syscall entries inside the sealed timed rounds; straced times are diagnostic.
import { createReadStream, existsSync, readFileSync, writeFileSync } from 'node:fs';
import { createInterface } from 'node:readline';
import { resolve } from 'node:path';

const [directory, trace] = process.argv.slice(2);
if (!directory || !trace) {
  process.stderr.write('Usage: node scripts/count-syscalls.mjs OUTPUT STRACE_FILE\n');
  process.exit(1);
}
const root = resolve(directory);
const clientFile = resolve(root, 'client.json');
const client = existsSync(clientFile) ? JSON.parse(readFileSync(clientFile, 'utf8')) : null;
const threads = client ? { suite: client.tids } : JSON.parse(readFileSync(resolve(root, 'threads.json'), 'utf8'));
const ops = { push: 200, clone: 50, read: 2000, write: 200 };
const windows = client ? client.rows.map(row => ({ workload: row.workload, start: row.start_epoch, end: row.end_epoch })) : [];
if (!client) {
  const phases = JSON.parse(readFileSync(resolve(root, 'phases.json'), 'utf8'));
  for (const phase of phases) {
    const match = /\[bench\] (push|clone|read|write) round.*?: ([\d.]+) ms/.exec(phase.line);
    if (match) windows.push({ workload: match[1], start: phase.time - Number(match[2]) / 1000, end: phase.time });
  }
}
const owner = new Map(Object.entries(threads).flatMap(([service, ids]) => ids.map(tid => [tid, service])));
const counts = Object.fromEntries(Object.keys(threads).map(s => [s, Object.fromEntries(Object.keys(ops).map(w => [w, {}]))]));
for await (const line of createInterface({ input: createReadStream(trace), crlfDelay: Infinity })) {
  const match = /^(\d+)\s+(\d+\.\d+)\s+([A-Za-z_][A-Za-z_0-9]*)\(/.exec(line);
  if (!match) continue;
  const service = owner.get(Number(match[1]));
  if (!service) continue;
  const time = Number(match[2]);
  const span = windows.find(w => time >= w.start && time <= w.end);
  if (!span) continue;
  const calls = counts[service][span.workload];
  calls[match[3]] = (calls[match[3]] ?? 0) + 1;
}
const result = {};
for (const [service, work] of Object.entries(counts)) {
  result[service] = {};
  for (const [workload, calls] of Object.entries(work)) {
    const n = ops[workload] * windows.filter(w => w.workload === workload).length;
    const total = Object.values(calls).reduce((a, b) => a + b, 0);
    const perOp = n ? total / n : null;
    result[service][workload] = { ops: n, total, per_op: perOp, calls };
    process.stdout.write(`${service} ${workload}: ${perOp?.toFixed(3) ?? 'unmeasured'} syscalls/op\n`);
  }
}
writeFileSync(resolve(root, 'syscalls.json'), JSON.stringify(result, null, 2) + '\n');
