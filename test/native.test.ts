import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawn, type ChildProcess } from 'node:child_process';
import { createServer } from 'node:http';
import { connect } from 'node:net';
import { cpSync, existsSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('../', import.meta.url));
const arch = process.arch === 'x64' ? 'amd64' : process.arch;
const binary = join(root, 'bin', `forge-linux-${arch}`);
const token = 'native-regression-token';

async function fixture(entry: string, native: boolean, check: (base: string, child: ChildProcess) => Promise<void>, expectedExit = 0): Promise<void> {
  let finish = false;
  let registrations = 0;
  let registered!: (port: number) => void;
  const portReady = new Promise<number>(resolve => { registered = resolve; });
  const env = createServer((req, res) => {
    if (req.url === '/register') {
      const parts: Buffer[] = [];
      req.on('data', bytes => parts.push(bytes));
      req.on('end', () => {
        registrations++;
        const { port } = JSON.parse(Buffer.concat(parts).toString());
        assert.notEqual(port, 8080);
        registered(port);
        res.end('{}');
      });
    } else res.end(JSON.stringify({ done: finish }));
  });
  await new Promise<void>(resolve => env.listen(0, '127.0.0.1', resolve));
  const address = env.address(); assert.ok(address && typeof address !== 'string');
  const child = spawn(process.execPath, [entry], { env: { ...process.env,
    FORGE_TS_FALLBACK: '0', MAKEFUN_ENV_URL: `http://127.0.0.1:${address.port}`,
    MAKEFUN_SHOP_TOKEN: token }, stdio: ['ignore', 'pipe', 'pipe'] });
  let stdout = '', stderr = '';
  child.stdout.on('data', bytes => { stdout += bytes; });
  child.stderr.on('data', bytes => { stderr += bytes; });
  const closed = new Promise<number | null>((resolve, reject) => { child.once('error', reject); child.once('close', resolve); });
  try {
    const port = await Promise.race([portReady, closed.then(code => { throw new Error(`entry exited ${code}: ${stderr}`); })]);
    await check(`http://127.0.0.1:${port}`, child);
    finish = true;
    assert.equal(await closed, expectedExit, stderr);
    assert.equal(registrations, 1);
    assert.equal(stdout.includes('[entry] native '), native, stdout + stderr);
  } finally {
    child.kill();
    env.closeAllConnections();
    await new Promise<void>(resolve => env.close(() => resolve()));
  }
}

test('native launcher serves shared REST, GraphQL, MCP and invalidates cached reads', { skip: !existsSync(binary), timeout: 15000 }, async () => {
  await fixture(join(root, 'main.ts'), true, async base => {
    const headers = { authorization: `Bearer ${token}`, 'content-type': 'application/json' };
    const call = async (path: string, body?: unknown, method = body === undefined ? 'GET' : 'POST') => {
      const response = await fetch(base + path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) });
      const value = response.status === 204 ? (await response.arrayBuffer(), null) : await response.json();
      return { response, value };
    };
    const repo = await call('/api/v3/user/repos', { name: 'native-shared' });
    assert.equal(repo.response.status, 201);
    const created = await call('/mcp', { jsonrpc: '2.0', id: 'native-issue', method: 'tools/call',
      params: { name: 'create_issue', arguments: { owner: 'mubudot', repo: 'native-shared', title: 'Unicode 猫 and "quotes"', body: 'λ\n☃' } } });
    assert.equal(created.value.id, 'native-issue');
    const issue = created.value.result.structuredContent;
    const path = `/api/v3/repos/mubudot/native-shared/issues/${issue.number}`;
    assert.deepEqual((await call(path)).value, issue);
    await call(path, { title: 'Changed', state: 'closed' }, 'PATCH');
    await call(path + '/comments', { body: 'Count this comment' });
    const changed = await call(path);
    assert.equal(changed.value.title, 'Changed');
    assert.equal(changed.value.comments, 1);
    assert.ok(changed.response.headers.get('x-github-request-id'));
    const graph = await call('/api/graphql', { query: '{ repository(owner:"mubudot",name:"native-shared"){issue(number:1){id title state}}}' });
    assert.equal(graph.value.data.repository.issue.id, issue.node_id);
    assert.equal(graph.value.data.repository.issue.state, 'CLOSED');
    assert.equal((await call('/api/v3/repos/other/native-shared')).response.status, 404);
    await call('/api/v3/repos/mubudot/native-shared', undefined, 'DELETE');
    await call('/api/v3/user/repos', { name: 'native-shared' });
    assert.equal((await call(path)).response.status, 404);
  });
});

test('native HTTP handles fragmented chunked input and pipelined output', { skip: !existsSync(binary), timeout: 15000 }, async () => {
  await fixture(join(root, 'main.ts'), true, async base => {
    const port = Number(new URL(base).port);
    const auth = `Host: localhost\r\nAuthorization: Bearer ${token}\r\n`;
    const json = JSON.stringify({ name: 'chunked-native', description: 'Unicode 猫' });
    const bytes = Buffer.from(json);
    const input = Buffer.concat([Buffer.from('POST /api/v3/user/repos HTTP/1.1\r\n' + auth + 'Transfer-Encoding: chunked\r\n\r\n' + bytes.length.toString(16) + '\r\n'), bytes,
      Buffer.from('\r\n0\r\n\r\nGET /api/v3/repos/mubudot/chunked-native HTTP/1.1\r\n' + auth + 'Connection: close\r\n\r\n')]);
    const output = await new Promise<Buffer>((resolve, reject) => {
      const socket = connect({ host: '127.0.0.1', port, noDelay: true });
      const parts: Buffer[] = [];
      socket.on('error', reject); socket.on('data', data => parts.push(data));
      socket.on('end', () => resolve(Buffer.concat(parts)));
      socket.on('connect', () => {
        let at = 0;
        const next = () => { if (at === input.length) return; const end = Math.min(input.length, at + 17); socket.write(input.subarray(at, end)); at = end; setImmediate(next); };
        next();
      });
    });
    let at = 0;
    for (const status of [201, 200]) {
      const end = output.indexOf('\r\n\r\n', at); assert.ok(end >= 0);
      const head = output.toString('latin1', at, end);
      assert.equal(Number(head.split(' ')[1]), status);
      const size = Number(/content-length:\s*(\d+)/i.exec(head)?.[1]);
      const body = JSON.parse(output.toString('utf8', end + 4, end + 4 + size));
      assert.equal(body.description, 'Unicode 猫');
      at = end + 4 + size;
    }
    assert.equal(at, output.length);
  });
});

for (const mode of ['missing', 'unexecutable', 'invalid']) {
  test(`launcher falls back when native executable is ${mode}`, { timeout: 15000 }, async () => {
    const directory = mkdtempSync(join(tmpdir(), 'forge-launcher-'));
    try {
      for (const path of ['main.ts', 'fallback.ts', 'app.ts', 'package.json', 'src']) cpSync(join(root, path), join(directory, path), { recursive: true });
      if (mode !== 'missing') {
        mkdirSync(join(directory, 'bin'));
        const header = Buffer.alloc(20);
        header.set([0x7f, 0x45, 0x4c, 0x46, 2, 1]);
        header.writeUInt16LE(arch === 'amd64' ? 62 : 183, 18);
        writeFileSync(join(directory, 'bin', `forge-linux-${arch}`), mode === 'invalid' ? 'invalid executable' : header,
          { mode: mode === 'invalid' ? 0o755 : 0o644 });
      }
      await fixture(join(directory, 'main.ts'), false, async base => {
        assert.deepEqual(await (await fetch(base + '/healthz')).json(), { ok: true });
      });
    } finally { rmSync(directory, { recursive: true, force: true }); }
  });
}

for (const signal of ['SIGTERM', 'SIGINT'] as const) {
  test(`launcher forwards ${signal} and preserves signal exit status`, { skip: !existsSync(binary), timeout: 15000 }, async () => {
    await fixture(join(root, 'main.ts'), true, async (_base, child) => {
      assert.equal(child.kill(signal), true);
    }, signal === 'SIGTERM' ? 143 : 130);
  });
}
