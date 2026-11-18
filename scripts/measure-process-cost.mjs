// Development only: unchanged sealed suite, with CPU counters outside its timers.
// Use a direct native executable so /proc/PID/stat measures the forge itself.
import { spawn } from 'node:child_process';
import { createHash } from 'node:crypto';
import { closeSync, mkdirSync, openSync, readFileSync, readSync, writeFileSync } from 'node:fs';
import { registerHooks } from 'node:module';
import { resolve } from 'node:path';
import { pathToFileURL } from 'node:url';

const [kitArg, outputArg, binaryArg] = process.argv.slice(2);
if (!kitArg || !outputArg || !binaryArg) {
  throw new Error('Usage: node [V8 flags] scripts/measure-process-cost.mjs KIT OUTPUT NATIVE_EXECUTABLE');
}
const kit = resolve(kitArg), output = resolve(outputArg), binary = resolve(binaryArg);
const header = Buffer.alloc(20), fd = openSync(binary, 'r');
let headerSize;
try { headerSize = readSync(fd, header, 0, header.length, 0); }
finally { closeSync(fd); }
const machine = process.arch === 'x64' ? 62 : process.arch === 'arm64' ? 183 : 0;
if (process.platform !== 'linux' || !machine || headerSize !== 20 ||
    header.subarray(0, 6).toString('hex') !== '7f454c460201' || header.readUInt16LE(18) !== machine) {
  throw new Error('Supply a native ELF executable matching this Linux host, without a launcher or tracing wrapper');
}
mkdirSync(output, { recursive: true });
const sealed = pathToFileURL(resolve(kit, 'program/env/server.mjs')).href;
const rows = [], phases = [];
const requestsPerRound = { push: 400, clone: 250, read: 2000, write: 200 };
let entry, before;
function forgeCPU() {
  const stat = readFileSync(`/proc/${entry.pid}/stat`, 'utf8');
  const fields = stat.slice(stat.lastIndexOf(')') + 2).trim().split(/\s+/);
  // Linux USER_HZ is 100 on the supported amd64/arm64 hosts and gVisor rig.
  return { user: Number(fields[11]) * 10, system: Number(fields[12]) * 10,
    threads: Number(fields[17]) };
}
const marker = Symbol.for('forge.measure-process-cost.round');
if (globalThis[marker]) throw new Error('Measurement hook already installed');
globalThis[marker] = (event, workload, round, ms) => {
  if (event === 'start') {
    before = { forge: forgeCPU(), client: process.cpuUsage() };
    before.epoch = (performance.timeOrigin + performance.now()) / 1000;
    return;
  }
  const epoch = (performance.timeOrigin + performance.now()) / 1000;
  const client = process.cpuUsage(before.client), forge = forgeCPU();
  rows.push({ workload, round: round + 1, wall_ms: ms,
    forge_cpu_ms: forge.user + forge.system - before.forge.user - before.forge.system,
    forge_user_ms: forge.user - before.forge.user,
    forge_system_ms: forge.system - before.forge.system,
    client_cpu_ms: (client.user + client.system) / 1000,
    client_user_ms: client.user / 1000, client_system_ms: client.system / 1000,
    threads: forge.threads, start_epoch: before.epoch, end_epoch: epoch });
};
const hooks = registerHooks({ load(url, context, next) {
  const result = next(url, context);
  if (url !== sealed) return result;
  let source = typeof result.source === 'string' ? result.source : Buffer.from(result.source).toString();
  let starts = 0, ends = 0;
  source = source.replace(/    const startedAt = run\.now\(\);/g, match => {
    starts++;
    return `    globalThis[Symbol.for('forge.measure-process-cost.round')]('start', run.spec.name, round);\n${match}`;
  });
  source = source.replace(/    run\.roundsMs\.push\(elapsed\);/g, match => {
    ends++;
    return `    globalThis[Symbol.for('forge.measure-process-cost.round')]('end', run.spec.name, round, elapsed);\n${match}`;
  });
  const tail = /var code = await main\(\);\nprocess\.exit\(code\);\s*$/;
  if (starts !== 4 || ends !== 4 || !tail.test(source)) throw new Error('Unknown sealed env layout');
  return { ...result, source: source.replace(tail, 'export { startEnv };\n') };
} });
let startEnv;
try { ({ startEnv } = await import(sealed)); }
finally { hooks.deregister(); }
const token = 'local-process-cost-token';
const env = await startEnv({ stateDir: output, host: '127.0.0.1', port: 0,
  env: { MAKEFUN_SHOP_TOKEN: token }, log: line => phases.push(line) });
entry = spawn(binary, [], { stdio: ['ignore', 'pipe', 'pipe'],
  env: { ...process.env, MAKEFUN_ENV_URL: `http://127.0.0.1:${env.address.port}`,
    MAKEFUN_SHOP_TOKEN: token, MAKEFUN_TRIAL_NOW: '2026-01-01T00:00:00Z' } });
let stdout = '', stderr = '';
entry.stdout.on('data', data => { stdout += data; });
entry.stderr.on('data', data => { stderr += data; });
const exited = new Promise((resolve, reject) => {
  entry.once('error', reject); entry.once('close', resolve);
});
try {
  const outcome = await Promise.race([env.done, exited.then(code => {
    throw new Error(`Native executable exited ${code} before the suite completed`);
  })]);
  const code = await exited;
  const clientMaxRSS = process.resourceUsage().maxRSS;
  const hash = data => createHash('sha256').update(data).digest('hex');
  const result = { passed: outcome.passed, total: outcome.total, child_exit_code: code,
    state_sha256: hash(readFileSync(resolve(output, 'export/state.json'))),
    binary_sha256: hash(readFileSync(binary)), sealed_sha256: hash(readFileSync(resolve(kit, 'program/env/server.mjs'))),
    versions: process.versions, execArgv: process.execArgv, clock_ticks_per_second: 100,
    client_max_rss_kib: clientMaxRSS, requests_per_round: requestsPerRound,
    perf: outcome.perf, rows };
  writeFileSync(resolve(output, 'cost.json'), JSON.stringify(result, null, 2) + '\n');
  const costs = Object.fromEntries(Object.entries(requestsPerRound).map(([name, requests]) => {
    const samples = rows.filter(row => row.workload === name);
    return [name, Object.fromEntries(['forge_cpu_ms', 'client_cpu_ms', 'wall_ms'].map(key =>
      [key.replace('_ms', '_us_per_request'), samples.reduce((sum, row) => sum + row[key], 0) * 1000 / (samples.length * requests)]))];
  }));
  console.log(JSON.stringify({ passed: outcome.passed, state_sha256: result.state_sha256,
    geomean_ms: outcome.perf.perf_p50_us / 1000, costs }));
  if (code !== 0 || outcome.passed !== 120 || rows.length !== 12 ||
      outcome.perf.workloads.length !== 4 || outcome.perf.workloads.some(w => w.error || w.rounds.length !== 3)) {
    process.exitCode = 1;
  }
} finally {
  if (entry.exitCode === null) entry.kill();
  await env.stop();
  writeFileSync(resolve(output, 'forge.stdout'), stdout);
  writeFileSync(resolve(output, 'forge.stderr'), stderr);
  writeFileSync(resolve(output, 'phases.json'), JSON.stringify(phases));
  delete globalThis[marker];
}
