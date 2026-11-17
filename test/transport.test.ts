import { test } from 'node:test';
import assert from 'node:assert/strict';
import { connect } from 'node:net';
import { createApp } from '../app.ts';
import { createTransport } from '../src/transport.ts';

function responses(bytes: Buffer): { status: number; body: Buffer }[] {
  const result = [];
  let at = 0;
  while (at < bytes.length) {
    const boundary = bytes.indexOf('\r\n\r\n', at);
    assert.ok(boundary >= 0);
    const head = bytes.toString('latin1', at, boundary);
    const size = Number(/content-length:\s*(\d+)/i.exec(head)?.[1] ?? 0);
    const start = boundary + 4;
    assert.ok(start + size <= bytes.length);
    result.push({ status: Number(head.split(' ')[1]), body: bytes.subarray(start, start + size) });
    at = start + size;
  }
  return result;
}

async function exchange(port: number, input: Buffer, fragment = 0): Promise<Buffer> {
  return new Promise((resolve, reject) => {
    const socket = connect({ port, host: '127.0.0.1', noDelay: true });
    const parts: Buffer[] = [];
    socket.on('data', bytes => parts.push(bytes));
    socket.on('error', reject);
    socket.on('end', () => resolve(Buffer.concat(parts)));
    socket.setTimeout(5000, () => socket.destroy(new Error('Response timed out')));
    socket.on('connect', () => {
      if (!fragment) socket.write(input);
      else {
        let at = 0;
        const next = () => {
          if (at === input.length) return;
          const end = Math.min(input.length, at + fragment);
          socket.write(input.subarray(at, end)); at = end;
          setImmediate(next);
        };
        next();
      }
    });
  });
}

test('TCP transport preserves fragmented bodies, pipelining, and mid-connection HTTP fallback', async () => {
  const previous = process.env.MAKEFUN_SHOP_TOKEN;
  process.env.MAKEFUN_SHOP_TOKEN = 'transport-test';
  const app = createApp();
  await new Promise<void>(resolve => app.listen(0, '127.0.0.1', resolve));
  const address = app.address(); assert.ok(address && typeof address !== 'string');
  const auth = 'host: localhost\r\nauthorization: Bearer transport-test\r\n';
  const body = JSON.stringify({ name: 'unicode', description: 'A snowman ☃' });
  const frame = 'GET /healthz HTTP/1.1\r\n' + auth + '\r\n' +
    'POST /api/v3/user/repos HTTP/1.1\r\n' + auth + 'content-type: application/json\r\n' +
    `content-length: ${Buffer.byteLength(body)}\r\n\r\n` + body +
    'GET /healthz HTTP/1.1\r\n' + auth + '\r\n' +
    'POST /api/v3/user/repos HTTP/1.1\r\n' + auth + 'transfer-encoding: chunked\r\n\r\n' +
    '12\r\n{"name":"chunked"}\r\n0\r\n\r\n' +
    'GET /api/v3/repos/mubudot/chunked HTTP/1.1\r\n' + auth + 'connection: close\r\n\r\n';
  try {
    const result = responses(await exchange(address.port, Buffer.from(frame), 19));
    assert.deepEqual(result.map(r => r.status), [200, 201, 200, 201, 200]);
    assert.equal(JSON.parse(result[1].body.toString()).description, 'A snowman ☃');
    assert.equal(JSON.parse(result[4].body.toString()).name, 'chunked');
  } finally {
    app.closeAllConnections();
    await new Promise<void>(resolve => app.close(() => resolve()));
    if (previous === undefined) delete process.env.MAKEFUN_SHOP_TOKEN; else process.env.MAKEFUN_SHOP_TOKEN = previous;
  }
});

test('TCP transport preserves response order when a pipelined reader applies backpressure', async () => {
  const body = Buffer.alloc(256 * 1024, 120);
  const server = createTransport((req, res) => {
    res.writeHead(200, { 'content-type': 'application/octet-stream', 'content-length': String(body.length) });
    res.end(body);
  });
  await new Promise<void>(resolve => server.listen(0, '127.0.0.1', resolve));
  const address = server.address(); assert.ok(address && typeof address !== 'string');
  try {
    const frame = 'GET / HTTP/1.1\r\nhost: localhost\r\n\r\n'.repeat(7) +
      'GET / HTTP/1.1\r\nhost: localhost\r\nconnection: close\r\n\r\n';
    const result = responses(await exchange(address.port, Buffer.from(frame)));
    assert.equal(result.length, 8);
    for (const response of result) assert.deepEqual(response.body, body);
  } finally {
    server.closeAllConnections();
    await new Promise<void>(resolve => server.close(() => resolve()));
  }
});
