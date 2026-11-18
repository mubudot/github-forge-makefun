// Development only: measure the sealed client after the full conformance phase.
// Hooks bracket existing timed rounds; no operation, request, or fixture changes.
import { spawn } from 'node:child_process';
import { createHash } from 'node:crypto';
import { subscribe, unsubscribe } from 'node:diagnostics_channel';
import { mkdirSync, readFileSync, readdirSync, writeFileSync } from 'node:fs';
import { registerHooks } from 'node:module';
import { resolve } from 'node:path';
import { PerformanceObserver } from 'node:perf_hooks';
import { fileURLToPath, pathToFileURL } from 'node:url';

const [kitArg, outputArg, executableArg] = process.argv.slice(2);
if (!kitArg || !outputArg) {
  throw new Error('Usage: node [client V8 flags] scripts/measure-client.mjs KIT OUTPUT [native executable]');
}
const kit = resolve(kitArg), output = resolve(outputArg);
const repo = fileURLToPath(new URL('../', import.meta.url));
mkdirSync(output, { recursive: true });
const useSDK = process.env.FORGE_MEASURE_SDK === '1';
const wireEnabled = process.env.FORGE_MEASURE_WIRE === '1';
const rows = [], phases = [], gc = [], subscriptions = [];
const socketIDs = new WeakMap();
let socketSequence = 0, current = null;

function listen(name, callback) {
  subscribe(name, callback);
  subscriptions.push([name, callback]);
}
function newWire() {
  return { requests: 0, connects: 0, sockets: new Set(), errors: [], responses: {},
    request_header_prefix_bytes: 0, request_body_bytes: 0, response_header_bytes: 0, response_body_bytes: 0 };
}
if (wireEnabled) {
  listen('undici:client:connected', ({ socket }) => {
    socketIDs.set(socket, ++socketSequence);
    if (current) current.wire.connects++;
  });
  listen('undici:client:sendHeaders', ({ request, headers, socket }) => {
    if (!current) return;
    const w = current.wire;
    w.requests++;
    w.sockets.add(socketIDs.get(socket));
    // This diagnostic precedes undici's addition of Content-Length and the
    // terminating CRLF. Fetch supplies its known payload length on request.
    w.request_header_prefix_bytes += Buffer.byteLength(headers);
    w.request_body_bytes += Number(request.contentLength ?? request.body?.byteLength ?? 0);
  });
  listen('undici:request:error', ({ error }) => {
    if (current) current.wire.errors.push(error.code ?? error.message);
  });
  listen('undici:request:headers', ({ request, response }) => {
    if (!current) return;
    const w = current.wire, headers = response.headers;
    let bytes = 2, length = 0;
    const names = [];
    for (let i = 0; i < headers.length; i += 2) {
      bytes += headers[i].length + headers[i + 1].length + 4;
      const name = headers[i].toString().toLowerCase();
      names.push(name);
      if (name === 'content-length') length = Number(headers[i + 1].toString());
    }
    // Header bytes exclude the status line. Response bodies here use Content-Length.
    w.response_header_bytes += bytes;
    w.response_body_bytes += length;
    const key = `${request.method} ${request.path}`;
    const item = w.responses[key] ??= { count: 0, min_body_bytes: length, max_body_bytes: length, headers: names };
    item.count++;
    item.min_body_bytes = Math.min(item.min_body_bytes, length);
    item.max_body_bytes = Math.max(item.max_body_bytes, length);
  });
}
const gcObserver = new PerformanceObserver(list => {
  for (const entry of list.getEntries()) gc.push({ start_ms: entry.startTime, duration_ms: entry.duration, kind: entry.detail.kind });
});
gcObserver.observe({ entryTypes: ['gc'] });

function mark(event, workload, round, ms) {
  if (event === 'start') {
    const memory = process.memoryUsage(), usage = process.resourceUsage();
    current = { workload, round: round + 1, memory_before: memory, usage_before: usage,
      loop_before: performance.nodeTiming.uvMetricsInfo, idle_before: performance.nodeTiming.idleTime,
      wire: wireEnabled ? newWire() : undefined, cpu_before: process.cpuUsage() };
    current.start_ms = performance.now();
    current.start_epoch = (performance.timeOrigin + current.start_ms) / 1000;
    return;
  }
  const end = performance.now(), cpu = process.cpuUsage(current.cpu_before);
  const usage = process.resourceUsage();
  const loop = performance.nodeTiming.uvMetricsInfo;
  rows.push({ workload, round: round + 1, elapsed_ms: ms,
    client_user_ms: cpu.user / 1000, client_system_ms: cpu.system / 1000,
    client_cpu_ms: (cpu.user + cpu.system) / 1000,
    start_ms: current.start_ms, end_ms: end,
    start_epoch: current.start_epoch, end_epoch: (performance.timeOrigin + end) / 1000,
    memory_before: current.memory_before, memory_after: process.memoryUsage(),
    minor_faults: usage.minorPageFault - current.usage_before.minorPageFault,
    voluntary_switches: usage.voluntaryContextSwitches - current.usage_before.voluntaryContextSwitches,
    involuntary_switches: usage.involuntaryContextSwitches - current.usage_before.involuntaryContextSwitches,
    loop_count: loop.loopCount - current.loop_before.loopCount,
    loop_events: loop.events - current.loop_before.events,
    loop_events_waiting: loop.eventsWaiting - current.loop_before.eventsWaiting,
    loop_idle_ms: performance.nodeTiming.idleTime - current.idle_before,
    ...(current.wire ? { wire: { ...current.wire, sockets: [...current.wire.sockets] } } : {}) });
  current = null;
}

const marker = Symbol.for('forge.measure-client.round');
if (globalThis[marker]) throw new Error('Client measurement hook already installed');
globalThis[marker] = mark;
const clientURL = pathToFileURL(resolve(kit, useSDK ? 'sdk/conformance/main.ts' : 'program/env/server.mjs')).href;
const benchURL = useSDK ? pathToFileURL(resolve(kit, 'sdk/conformance/bench.ts')).href : clientURL;
let loaded = false;
const hooks = registerHooks({ load(url, context, next) {
  const result = next(url, context);
  if (url !== benchURL) return result;
  let source = typeof result.source === 'string' ? result.source : Buffer.from(result.source).toString();
  let starts = 0, ends = 0;
  source = source.replace(/    const startedAt = run\.now\(\);/g, match => {
    starts++;
    return `    globalThis[Symbol.for('forge.measure-client.round')]('start', run.spec.name, round);\n${match}`;
  });
  source = source.replace(/    run\.roundsMs\.push\(elapsed\);/g, match => {
    ends++;
    return `    globalThis[Symbol.for('forge.measure-client.round')]('end', run.spec.name, round, elapsed);\n${match}`;
  });
  if (starts !== 4 || ends !== 4) throw new Error(`Unknown benchmark layout (${starts} starts, ${ends} ends)`);
  if (!useSDK) {
    // Import the production bundle as a library. Keep its suite, fixtures, and
    // clients intact; replace only the command-line invocation at the very end.
    const tail = /var code = await main\(\);\nprocess\.exit\(code\);\s*$/;
    if (!tail.test(source)) throw new Error('Unknown sealed env entrypoint');
    source = source.replace(tail, 'export { startEnv };\n');
  }
  loaded = true;
  return { ...result, source };
} });
let startEnv;
try { ({ startEnv } = await import(clientURL)); }
finally { hooks.deregister(); }
if (!loaded) throw new Error('The benchmark was not instrumented');

const token = 'local-client-measurement-token';
const env = await startEnv({ stateDir: output, host: '127.0.0.1', port: 0,
  env: { MAKEFUN_SHOP_TOKEN: token },
  log: line => phases.push({ time: (performance.timeOrigin + performance.now()) / 1000, line }) });
const program = executableArg ? resolve(executableArg) : process.execPath;
const child = spawn(program, executableArg ? [] : [resolve(repo, 'main.ts')], {
  cwd: repo, stdio: ['ignore', 'pipe', 'pipe'],
  env: { ...process.env, MAKEFUN_ENV_URL: `http://127.0.0.1:${env.address.port}`,
    MAKEFUN_SHOP_TOKEN: token, MAKEFUN_TRIAL_NOW: '2026-01-01T00:00:00Z' },
});
let stdout = '', stderr = '';
child.stdout.on('data', data => { stdout += data; });
child.stderr.on('data', data => { stderr += data; });
const closed = new Promise((resolve, reject) => { child.once('close', resolve); child.once('error', reject); });
try {
  const outcome = await env.done;
  const code = await closed;
  const state = readFileSync(resolve(output, 'export/state.json'));
  for (const row of rows) {
    const events = gc.filter(e => e.start_ms >= row.start_ms && e.start_ms < row.end_ms);
    row.gc_count = events.length;
    row.gc_ms = events.reduce((sum, e) => sum + e.duration_ms, 0);
  }
  const result = { pid: process.pid, tids: readdirSync('/proc/self/task').map(Number),
    versions: process.versions, execArgv: process.execArgv, wire_instrumented: wireEnabled,
    client_module: clientURL,
    profile_clock_offset_us: Number(process.hrtime.bigint() / 1000n) - performance.now() * 1000,
    passed: outcome.passed, child_exit_code: code,
    state_sha256: createHash('sha256').update(state).digest('hex'),
    perf: outcome.perf, rows, gc };
  writeFileSync(resolve(output, 'client.json'), JSON.stringify(result, null, 2) + '\n');
  process.stdout.write(JSON.stringify({ passed: outcome.passed, state_sha256: result.state_sha256,
    geomean_ms: outcome.perf.perf_p50_us / 1000, rounds: rows.map(r => ({ workload: r.workload,
      round: r.round, ms: r.elapsed_ms, cpu_ms: r.client_cpu_ms, gc_ms: r.gc_ms })) }) + '\n');
  if (code !== 0 || outcome.passed !== 120 || outcome.perf.workloads.length !== 4 ||
      outcome.perf.workloads.some(w => w.error !== undefined || w.rounds.length !== 3)) {
    process.exitCode = 1;
  }
} finally {
  await env.stop();
  writeFileSync(resolve(output, 'phases.json'), JSON.stringify(phases));
  writeFileSync(resolve(output, 'forge.stdout'), stdout);
  writeFileSync(resolve(output, 'forge.stderr'), stderr);
  if (child.exitCode === null) child.kill();
  gcObserver.disconnect();
  for (const [name, callback] of subscriptions) unsubscribe(name, callback);
  delete globalThis[marker];
}
