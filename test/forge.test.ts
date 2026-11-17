import { test } from 'node:test';
import assert from 'node:assert/strict';
import { deflateSync } from 'node:zlib';
import { createServer } from 'node:http';
import { spawn } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { createApp } from '../app.ts';
import { ObjectDatabase, hashObject, sha1 } from '../src/git/objects.ts';
import { readPack, writePack } from '../src/git/pack.ts';
import { Store } from '../src/store.ts';
import { execute } from '../src/graphql/executor.ts';

function header(code: number, size: number): Buffer {
  const out = [(code << 4) | (size & 15)];
  size = Math.floor(size / 16);
  while (size) { out[out.length - 1] |= 128; out.push(size & 127); size = Math.floor(size / 128); }
  return Buffer.from(out);
}

function pack(records: Buffer[]): Buffer {
  const head = Buffer.alloc(12);
  head.write('PACK'); head.writeUInt32BE(2, 4); head.writeUInt32BE(records.length, 8);
  const body = Buffer.concat([head, ...records]);
  return Buffer.concat([body, sha1(body)]);
}

test('packs resolve a forward REF delta chained through an OFS delta', () => {
  const base = Buffer.from('hello world\n'), second = Buffer.from('hello forge\n'), third = Buffer.from('hello forge!\n');
  const deltaSecond = Buffer.concat([Buffer.from([12, 12, 0x90, 6, 6]), Buffer.from('forge\n')]);
  const deltaThird = Buffer.concat([Buffer.from([12, 13, 0x90, 11, 2]), Buffer.from('!\n')]);
  const firstRecord = Buffer.concat([header(7, deltaThird.length), Buffer.from(hashObject('blob', second), 'hex'), deflateSync(deltaThird)]);
  const baseRecord = Buffer.concat([header(3, base.length), deflateSync(base)]);
  const secondRecord = Buffer.concat([header(6, deltaSecond.length), Buffer.from([baseRecord.length]), deflateSync(deltaSecond)]);
  const db = new ObjectDatabase();
  readPack(pack([firstRecord, baseRecord, secondRecord]), db);
  assert.equal(db.objects.size, 3);
  for (const data of [base, second, third]) assert.deepEqual(db.get(hashObject('blob', data)).data, data);
  const roundTrip = new ObjectDatabase();
  readPack(writePack([...db.objects.values()]), roundTrip);
  assert.deepEqual([...roundTrip.objects.keys()].sort(), [...db.objects.keys()].sort());
});

test('thin packs use external bases, and damaged packs expose no new objects', () => {
  const db = new ObjectDatabase(), base = Buffer.from('hello world\n');
  const oid = db.put('blob', base);
  const target = Buffer.from('hello forge\n');
  const delta = Buffer.concat([Buffer.from([12, 12, 0x90, 6, 6]), Buffer.from('forge\n')]);
  const bytes = pack([Buffer.concat([header(7, delta.length), Buffer.from(oid, 'hex'), deflateSync(delta)])]);
  const damaged = Buffer.from(bytes); damaged[damaged.length - 1] ^= 1;
  assert.throws(() => readPack(damaged, db), /checksum/);
  assert.equal(db.objects.size, 1);
  readPack(bytes, db);
  assert.deepEqual(db.get(hashObject('blob', target)).data, target);
});

test('GraphQL handles aliases, named fragments, defaults, directives, and mutation parity', () => {
  const store = new Store();
  store.login = 'mubudot';
  const repo = store.createRepo({ name: 'parser-test' });
  const created = execute(store, { query: 'mutation($input: CreateIssueInput!) { created:createIssue(input:$input) { issue { id number title } } }', variables: { input: { repositoryId: repo.node_id, title: 'Parser mutation' } } });
  assert.equal(created.data.created.issue.title, 'Parser mutation');
  const answer = execute(store, { query: `query Read($show: Boolean = true) {
    first: repository(owner:"mubudot", name:"parser-test") {
      ...RepoFields
      issues(first:1) { edges { cursor node { number title } } pageInfo { hasNextPage } }
    }
    viewer @include(if:$show) { login }
    hidden:viewer @skip(if:true) { login }
  } fragment RepoFields on Repository { id name }` });
  assert.equal(answer.errors, undefined);
  assert.equal(answer.data.first.id, repo.node_id);
  assert.equal(answer.data.first.issues.edges[0].node.title, 'Parser mutation');
  assert.equal(answer.data.viewer.login, 'mubudot');
  assert.equal('hidden' in answer.data, false);
  assert.match(execute(store, { query: '{ viewer { unknownField } }' }).errors[0].message, /unknownField/);
});

test('malformed HTTP requests stay contained and do not interrupt later requests', async () => {
  const originalToken = process.env.MAKEFUN_SHOP_TOKEN;
  process.env.MAKEFUN_SHOP_TOKEN = 'local-test-token';
  const app = createApp();
  await new Promise<void>(resolve => app.listen(0, '127.0.0.1', resolve));
  const address = app.address();
  assert.ok(address && typeof address !== 'string');
  const base = `http://127.0.0.1:${address.port}`;
  const headers = { authorization: 'Bearer local-test-token', 'content-type': 'application/json' };
  try {
    const unauthorized = await fetch(`${base}/api/v3/user`);
    assert.equal(unauthorized.status, 401); await unauthorized.arrayBuffer();
    const malformed = await fetch(`${base}/api/v3/user/repos`, { method: 'POST', headers, body: '{"name":' });
    assert.equal(malformed.status, 400);
    assert.ok(malformed.headers.get('x-github-request-id'));
    assert.equal(typeof (await malformed.json()).message, 'string');
    const badRpc = await fetch(`${base}/mcp`, { method: 'POST', headers, body: '{broken' });
    assert.equal(badRpc.status, 400);
    assert.equal((await badRpc.json()).error.code, -32700);
    const rpc = await fetch(`${base}/mcp`, { method: 'POST', headers, body: JSON.stringify({ jsonrpc: '2.0', id: 'missing', method: 'tools/call', params: { name: 'create_issue', arguments: {} } }) });
    assert.equal((await rpc.json()).error.code, -32602);
    const unknown = await fetch(`${base}/api/v3/unknown`, { headers });
    assert.equal(unknown.status, 404); await unknown.arrayBuffer();
    const health = await fetch(`${base}/healthz`);
    assert.deepEqual(await health.json(), { ok: true });
  } finally {
    app.closeAllConnections();
    await new Promise<void>(resolve => app.close(() => resolve()));
    if (originalToken === undefined) delete process.env.MAKEFUN_SHOP_TOKEN; else process.env.MAKEFUN_SHOP_TOKEN = originalToken;
  }
});

test('hot routes preserve auth, invalidation, and repository identity after recreation', async () => {
  const originalToken = process.env.MAKEFUN_SHOP_TOKEN;
  process.env.MAKEFUN_SHOP_TOKEN = 'local-test-token';
  const app = createApp();
  await new Promise<void>(resolve => app.listen(0, '127.0.0.1', resolve));
  const address = app.address();
  assert.ok(address && typeof address !== 'string');
  const base = `http://127.0.0.1:${address.port}/api/v3`;
  const headers = { authorization: 'Bearer local-test-token', 'content-type': 'application/json' };
  const request = async (path: string, method = 'GET', body?: any) => {
    const response = await fetch(base + path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) });
    const value = response.status === 204 ? (await response.arrayBuffer(), null) : await response.json();
    return { status: response.status, value };
  };
  try {
    const repo = '/repos/mubudot/hot-route-test';
    assert.equal((await request('/user/repos', 'POST', { name: 'hot-route-test' })).status, 201);
    const title = 'Quotes " and newline\nwith Unicode: é猫';
    const created = await request(repo + '/issues', 'POST', { title, body: 'Unicode bytes: λ猫' });
    assert.equal(created.status, 201);
    const path = repo + '/issues/1';
    assert.deepEqual((await request(path)).value, created.value);
    const unauthorized = await fetch(base + path);
    assert.equal(unauthorized.status, 401); await unauthorized.arrayBuffer();
    assert.equal((await request('/repos/other-owner/hot-route-test/issues/1')).status, 404);
    assert.equal((await request(path, 'PATCH', { state: 'closed', title: 'Updated' })).status, 200);
    const changed = (await request(path)).value;
    assert.equal(changed.state, 'closed'); assert.equal(changed.title, 'Updated');
    assert.equal((await request(path + '/comments', 'POST', { body: 'A comment' })).status, 201);
    assert.equal((await request(path)).value.comments, 1);
    assert.equal((await request(repo, 'DELETE')).status, 204);
    assert.equal((await request(path)).status, 404);
    assert.equal((await request('/user/repos', 'POST', { name: 'hot-route-test' })).status, 201);
    assert.equal((await request(path)).status, 404);
    const replacement = await request(repo + '/issues', 'POST', { title: 'Replacement' });
    assert.notEqual(replacement.value.id, created.value.id);
    assert.equal((await request(path)).value.title, 'Replacement');
  } finally {
    app.closeAllConnections();
    await new Promise<void>(resolve => app.close(() => resolve()));
    if (originalToken === undefined) delete process.env.MAKEFUN_SHOP_TOKEN; else process.env.MAKEFUN_SHOP_TOKEN = originalToken;
  }
});

test('the lifecycle retries registration and malformed polls, then exits only on done', { timeout: 12000 }, async () => {
  let registrations = 0, polls = 0, health = false;
  const env = createServer((req, res) => {
    if (req.url === '/register') {
      const chunks: Buffer[] = [];
      req.on('data', chunk => chunks.push(chunk));
      req.on('end', () => {
        registrations++;
        if (registrations === 1) { res.writeHead(503); res.end('{}'); return; }
        const { port } = JSON.parse(Buffer.concat(chunks).toString());
        void fetch(`http://127.0.0.1:${port}/healthz`).then(async response => {
          health = response.status === 200 && (await response.json()).ok === true;
          res.end('{}');
        });
      });
    } else {
      polls++;
      res.end(polls === 1 ? '{"done":false}' : polls === 2 ? '{invalid' : '{"done":true}');
    }
  });
  await new Promise<void>(resolve => env.listen(0, '127.0.0.1', resolve));
  const address = env.address();
  assert.ok(address && typeof address !== 'string');
  const child = spawn(process.execPath, [fileURLToPath(new URL('../main.ts', import.meta.url))], {
    env: { ...process.env, MAKEFUN_ENV_URL: `http://127.0.0.1:${address.port}`, MAKEFUN_SHOP_TOKEN: 'local-test-token' },
    stdio: ['ignore', 'ignore', 'pipe'],
  });
  let stderr = '';
  child.stderr.on('data', chunk => { stderr += chunk; });
  try {
    const code = await new Promise<number | null>((resolve, reject) => { child.on('error', reject); child.on('close', resolve); });
    assert.equal(code, 0, stderr);
    assert.equal(registrations, 2); assert.equal(polls, 3); assert.equal(health, true);
  } finally {
    child.kill();
    env.closeAllConnections();
    await new Promise<void>(resolve => env.close(() => resolve()));
  }
});
