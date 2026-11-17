// Development only: drive the unmodified SDK suite, optionally under strace/runsc.
import { spawn } from 'node:child_process';
import { createHash } from 'node:crypto';
import { mkdirSync, readFileSync, readdirSync, writeFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const [kit, output, ...nodeArgs] = process.argv.slice(2);
if (!kit || !output) {
  process.stderr.write('Usage: node scripts/measure-speed.mjs KIT OUTPUT [forge Node flags...]\n');
  process.exit(1);
}
const repo = fileURLToPath(new URL('../', import.meta.url));
const root = resolve(output);
mkdirSync(root, { recursive: true });
const { startEnv } = await import(pathToFileURL(resolve(kit, 'sdk/conformance/main.ts')).href);
const token = 'local-measurement-token';
const phases = [];
const env = await startEnv({ stateDir: root, host: '127.0.0.1', port: 0,
  env: { MAKEFUN_SHOP_TOKEN: token },
  log: line => phases.push({ time: (performance.timeOrigin + performance.now()) / 1000, line }) });
const child = spawn(process.execPath, [...nodeArgs, resolve(repo, 'main.ts')], {
  cwd: repo, stdio: ['ignore', 'ignore', 'pipe'],
  env: { ...process.env, MAKEFUN_ENV_URL: `http://127.0.0.1:${env.address.port}`,
    MAKEFUN_SHOP_TOKEN: token, MAKEFUN_TRIAL_NOW: '2026-01-01T00:00:00Z' },
});
let stderr = '';
child.stderr.on('data', data => { stderr += data; });
const closed = new Promise(resolve => child.on('close', resolve));
const threads = pid => { try { return readdirSync(`/proc/${pid}/task`).map(Number); } catch { return [pid]; } };
try {
  const outcome = await env.done;
  writeFileSync(resolve(root, 'threads.json'), JSON.stringify({ suite: threads(process.pid), forge: threads(child.pid) }));
  const code = await closed;
  const state = readFileSync(resolve(root, 'export/state.json'));
  const result = { passed: outcome.passed, child_exit_code: code,
    state_sha256: createHash('sha256').update(state).digest('hex'),
    workloads: outcome.perf.workloads.map(w => ({ name: w.name, ms: w.p50_ms, rounds: w.rounds, points: w.points, error: w.error })),
    peak_rss_KiB: Number(/peak RSS (\d+) KiB/.exec(stderr)?.[1] ?? 0) };
  writeFileSync(resolve(root, 'result.json'), JSON.stringify(result, null, 2) + '\n');
  process.stdout.write(JSON.stringify(result) + '\n');
  if (code !== 0 || outcome.passed !== 120) process.exitCode = 1;
} finally {
  await env.stop();
  writeFileSync(resolve(root, 'phases.json'), JSON.stringify(phases));
  writeFileSync(resolve(root, 'forge.stderr'), stderr);
  if (child.exitCode === null) child.kill();
}
