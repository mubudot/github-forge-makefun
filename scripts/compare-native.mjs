// Development only. Run unchanged SDK workloads sequentially in alternating
// orders; never compare scores collected while compiling or profiling.
import { spawn } from 'node:child_process';
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
const [kit, output, count = '3'] = process.argv.slice(2);
if (!kit || !output) throw new Error('Usage: node scripts/compare-native.mjs KIT OUTPUT [REPEATS]');
const repo = fileURLToPath(new URL('../', import.meta.url));
const root = resolve(output);
mkdirSync(root, { recursive: true });
const results = [];
const variants = (process.env.FORGE_BENCH_VARIANTS ?? 'typescript,blocking,netpoll').split(',');
if (variants.some(variant => !['typescript', 'blocking', 'netpoll'].includes(variant))) throw new Error('Unknown comparison variant');
for (let round = 0; round < Number(count); round++) {
  const order = round % 2 ? [...variants].reverse() : variants;
  for (const variant of order) {
    const dest = resolve(root, `${round + 1}-${variant}`);
    const env = { ...process.env, FORGE_TS_FALLBACK: variant === 'typescript' ? '1' : '0',
      FORGE_TRANSPORT: variant === 'netpoll' ? 'netpoll' : 'blocking' };
    delete env.FORGE_CPU_PROFILE;
    delete env.FORGE_HEAP_PROFILE;
    const child = spawn(process.execPath, [resolve(repo, 'scripts/measure-speed.mjs'), resolve(kit), dest], { cwd: repo, env, stdio: ['ignore', 'ignore', 'inherit'] });
    const code = await new Promise((resolve, reject) => { child.once('error', reject); child.once('close', resolve); });
    if (code !== 0) throw new Error(`${variant} exited ${code}`);
    const result = { round: round + 1, variant, ...JSON.parse(readFileSync(resolve(dest, 'result.json'), 'utf8')) };
    if (result.state_sha256 !== 'a0e98d49854b961230227867235c8615858e006db3763f341ac6154af1445590') throw new Error(`${variant}: state differs`);
    results.push(result);
    process.stdout.write(JSON.stringify(result) + '\n');
    writeFileSync(resolve(root, 'comparison.json'), JSON.stringify(results, null, 2) + '\n');
  }
}
const median = values => [...values].sort((a, b) => a - b)[Math.floor(values.length / 2)];
const summary = variants.map(variant => {
  const rows = results.filter(row => row.variant === variant);
  const workloads = Object.fromEntries(['push', 'clone', 'read', 'write'].map(name => [name, median(rows.map(row => row.workloads.find(w => w.name === name).ms))]));
  const geomean_ms = Math.exp(Object.values(workloads).reduce((sum, ms) => sum + Math.log(ms), 0) / 4);
  return { variant, workloads, geomean_ms, peak_rss_KiB: Math.max(...rows.map(row => row.peak_rss_KiB)) };
});
writeFileSync(resolve(root, 'summary.json'), JSON.stringify(summary, null, 2) + '\n');
process.stdout.write(JSON.stringify(summary, null, 2) + '\n');
