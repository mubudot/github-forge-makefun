// Native core supervisor. The selected server alone owns the env lifecycle.
import { spawn } from 'node:child_process';
import { openSync, readSync, closeSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

// Some spawn implementations pass an unrecognized executable to a shell.
// Check the ELF header first so a damaged or foreign binary also uses fallback.
function checkExecutable(path: string, arch: string): void {
  if (process.platform !== 'linux') throw new Error('native core requires Linux');
  const fd = openSync(path, 'r');
  try {
    const header = Buffer.alloc(20);
    const machine = arch === 'amd64' ? 62 : arch === 'arm64' ? 183 : 0;
    if (!machine || readSync(fd, header, 0, header.length, 0) !== header.length ||
      header.toString('hex', 0, 6) !== '7f454c460201' || header.readUInt16LE(18) !== machine) {
      throw new Error('native executable has an incompatible ELF header');
    }
  } finally { closeSync(fd); }
}

async function fallback(error: unknown): Promise<void> {
  process.stderr.write(`[entry] native executable unavailable (${error instanceof Error ? error.message : String(error)}); using TypeScript\n`);
  await import('./fallback.ts');
}

async function launch(): Promise<void> {
  if (process.env.FORGE_TS_FALLBACK === '1') { await import('./fallback.ts'); return; }
  const arch = process.arch === 'x64' ? 'amd64' : process.arch;
  const executable = fileURLToPath(new URL(`./bin/forge-linux-${arch}`, import.meta.url));
  try { checkExecutable(executable, arch); }
  catch (error) { await fallback(error); return; }
  const child = spawn(executable, [], { env: process.env, stdio: 'inherit' });
  const term = () => child.kill('SIGTERM');
  const interrupt = () => child.kill('SIGINT');
  process.on('SIGTERM', term);
  process.on('SIGINT', interrupt);
  const cleanup = () => { process.off('SIGTERM', term); process.off('SIGINT', interrupt); };
  await new Promise<void>((resolve, reject) => {
    let failed = false;
    child.once('error', error => {
      failed = true;
      cleanup();
      fallback(error).then(() => resolve(), reject);
    });
    child.once('exit', (code, signal) => {
      if (failed) return;
      cleanup();
      process.exit(code ?? (signal === 'SIGINT' ? 130 : signal === 'SIGTERM' ? 143 : 1));
    });
  });
}

await launch();
