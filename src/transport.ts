import { createServer as createHTTPServer, STATUS_CODES, type IncomingMessage, type ServerResponse, type Server as HTTPServer } from 'node:http';
import { createServer as createTCPServer, type Socket, type Server as TCPServer } from 'node:net';
import type { BufferedRequest, BufferedWriter } from './http.ts';

const EMPTY = Buffer.alloc(0);
const MAX_BODY = 32 * 1024 * 1024;
const MAX_HEAD = 16 * 1024;
const prefixes = new Map<string, string>();
export type BufferedServer = TCPServer & Pick<HTTPServer, 'closeIdleConnections' | 'closeAllConnections'>;
type Handler = (req: IncomingMessage, res: ServerResponse) => void;

/** A buffered response needs no stream, chunk encoder, finish task, or idle timer. */
class BufferedResponse implements BufferedWriter {
  socket: Socket;
  req: IncomingMessage;
  writableEnded = false;
  sendDate = false;
  status = 200;
  headers = new Map<string, string>();
  complete: () => void;
  constructor(socket: Socket, req: IncomingMessage, complete: () => void) {
    this.socket = socket; this.req = req; this.complete = complete;
  }
  get destroyed(): boolean { return this.socket.destroyed; }
  setHeader(name: string, value: string | number): void { this.headers.set(name.toLowerCase(), String(value)); }
  getHeader(name: string): string | undefined { return this.headers.get(name.toLowerCase()); }
  removeHeader(name: string): void { this.headers.delete(name.toLowerCase()); }
  writeHead(status: number, headers: string[] | Record<string, string>): this {
    this.status = status;
    if (Array.isArray(headers)) {
      for (let i = 0; i < headers.length; i += 2) this.setHeader(headers[i], headers[i + 1]);
    } else for (const name in headers) this.setHeader(name, headers[name]);
    return this;
  }
  sendBuffered(status: number, body: string | Buffer, type: string, length: number, id: number, headers?: Record<string, string>): void {
    if (headers || this.headers.size) {
      this.writeHead(status, { 'content-type': type, 'content-length': String(length), 'x-github-request-id': id.toString(16), ...headers });
      this.end(body);
      return;
    }
    if (this.writableEnded || this.destroyed) return;
    this.writableEnded = true;
    const key = `${status}:${type}:${length}`;
    let prefix = prefixes.get(key);
    if (!prefix) {
      prefix = `HTTP/1.1 ${status} ${STATUS_CODES[status] ?? 'Response'}\r\ncontent-type: ${type}\r\ncontent-length: ${length}\r\nkeep-alive: timeout=65\r\nx-github-request-id: `;
      if (prefixes.size >= 256) prefixes.clear();
      prefixes.set(key, prefix);
    }
    let head = prefix + id.toString(16);
    const close = this.req.headers.connection?.toLowerCase() === 'close';
    head += close ? '\r\nconnection: close\r\n\r\n' : '\r\n\r\n';
    const payload = this.req.method === 'HEAD' || status === 204 || status === 304 ? EMPTY : body;
    const frame = typeof payload === 'string' ? head + payload : Buffer.concat([Buffer.from(head, 'latin1'), payload]);
    if (close) this.socket.end(frame); else this.socket.write(frame);
    this.complete();
  }
  end(body: string | Buffer = EMPTY): void {
    if (this.writableEnded || this.destroyed) return;
    this.writableEnded = true;
    let head = `HTTP/1.1 ${this.status} ${STATUS_CODES[this.status] ?? 'Response'}\r\n`;
    for (const [key, value] of this.headers) head += `${key}: ${value}\r\n`;
    const close = this.req.headers.connection?.toLowerCase() === 'close';
    if (close) head += 'connection: close\r\n';
    if (!this.headers.has('content-length')) head += `content-length: ${Buffer.byteLength(body)}\r\n`;
    head += 'keep-alive: timeout=65\r\n\r\n';
    const payload = this.req.method === 'HEAD' || this.status === 204 || this.status === 304 ? EMPTY : body;
    const frame = typeof payload === 'string' ? head + payload : Buffer.concat([Buffer.from(head, 'latin1'), payload]);
    if (close) this.socket.end(frame);
    else this.socket.write(frame);
    this.complete();
  }
}

/**
 * The forge buffers every response and request body. Serve ordinary HTTP/1.1
 * directly, and hand uncommon framing to Node's HTTP server on the same socket.
 * Both transports use the very same handler and store.
 */
export function createTransport(handler: Handler): BufferedServer {
  const fallback = createHTTPServer({ noDelay: true }, handler);
  fallback.keepAliveTimeout = 65000;
  fallback.headersTimeout = 70000;
  let generation = 0;
  const sockets = new Map<Socket, { busy: boolean; fallback: boolean; active: number }>();
  const server = createTCPServer({ noDelay: true }, socket => {
    const state = { busy: false, fallback: false, active: generation };
    sockets.set(socket, state);
    let pending = EMPTY, head: IncomingMessage | undefined;
    let remaining = 0, bodySize = 0, bodyParts: Buffer[] = [];
    let handedOff = false, pumping = false;
    let cachedHead = EMPTY, cachedRequest: IncomingMessage | undefined;
    socket.on('error', () => {});
    socket.on('close', () => sockets.delete(socket));
    const handOff = () => {
      handedOff = state.fallback = true;
      socket.pause();
      socket.removeListener('data', onData);
      if (pending.length) socket.unshift(pending);
      fallback.emit('connection', socket);
      socket.resume();
    };
    const processRequest = (req: IncomingMessage, body: Buffer) => {
      head = undefined; bodyParts = []; bodySize = 0;
      (req as BufferedRequest).bufferedBody = body;
      state.busy = true;
      const res = new BufferedResponse(socket, req, () => {
        if (socket.writableNeedDrain) socket.once('drain', () => { state.busy = false; pump(); });
        else { state.busy = false; pump(); }
      });
      try { handler(req, res as unknown as ServerResponse); }
      catch { socket.destroy(); }
    };
    const pump = () => {
      if (pumping || handedOff || socket.destroyed) return;
      pumping = true;
      try {
        while (!state.busy && !socket.writableEnded) {
          if (!head && cachedRequest && pending.length >= cachedHead.length && pending.subarray(0, cachedHead.length).equals(cachedHead)) {
            head = cachedRequest;
            remaining = Number(head.headers['content-length'] ?? 0);
            pending = pending.subarray(cachedHead.length);
          }
          if (!head) {
            const boundary = pending.indexOf('\r\n\r\n');
            if (boundary < 0) { if (pending.length > MAX_HEAD) handOff(); break; }
            if (boundary > MAX_HEAD) { handOff(); break; }
            const lines = pending.toString('latin1', 0, boundary).split('\r\n');
            const first = /^(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS) (\S+) HTTP\/1\.1$/.exec(lines[0]);
            if (!first) { handOff(); break; }
            const headers: Record<string, string> = Object.create(null), rawHeaders: string[] = [];
            let valid = true;
            for (let i = 1; i < lines.length; i++) {
              const colon = lines[i].indexOf(':');
              if (colon < 1) { valid = false; break; }
              const name = lines[i].slice(0, colon), key = name.toLowerCase(), value = lines[i].slice(colon + 1).trim();
              if (!/^[!#$%&'*+.^_`|~0-9a-z-]+$/.test(key) || /[\x00-\x08\x0a-\x1f\x7f]/.test(value) || headers[key] !== undefined) { valid = false; break; }
              headers[key] = value; rawHeaders.push(name, value);
            }
            if (!valid || headers['transfer-encoding'] || headers.expect || headers.upgrade || !headers.host ||
                (headers['content-length'] !== undefined && !/^\d+$/.test(headers['content-length']))) { handOff(); break; }
            remaining = Number(headers['content-length'] ?? 0);
            if (!Number.isSafeInteger(remaining) || remaining > MAX_BODY) { handOff(); break; }
            head = { method: first[1], url: first[2], headers, rawHeaders, socket,
              httpVersion: '1.1', httpVersionMajor: 1, httpVersionMinor: 1 } as unknown as IncomingMessage;
            cachedHead = Buffer.from(pending.subarray(0, boundary + 4));
            cachedRequest = head;
            pending = pending.subarray(boundary + 4);
          }
          if (!remaining) { processRequest(head, EMPTY); continue; }
          if (pending.length >= remaining) {
            const last = pending.subarray(0, remaining);
            pending = pending.subarray(remaining);
            remaining = 0;
            const body = bodyParts.length ? Buffer.concat([...bodyParts, last], bodySize + last.length) : last;
            processRequest(head, body);
          } else {
            if (pending.length) { bodyParts.push(pending); bodySize += pending.length; remaining -= pending.length; pending = EMPTY; }
            break;
          }
        }
      } finally { pumping = false; }
    };
    const onData = (bytes: Buffer) => {
      state.active = generation;
      pending = pending.length ? Buffer.concat([pending, bytes]) : bytes;
      if (pending.length > MAX_BODY + MAX_HEAD) { socket.destroy(); return; }
      pump();
    };
    socket.on('data', onData);
  }) as BufferedServer;
  // One shared sweep replaces per-request timer refreshes. A connection remains
  // idle for at least the advertised 65 seconds before this sweep closes it.
  const sweep = setInterval(() => {
    generation++;
    for (const [socket, state] of sockets) {
      if (!state.fallback && !state.busy && state.active < generation - 1) socket.end();
    }
  }, 65000);
  sweep.unref();
  server.once('close', () => clearInterval(sweep));
  server.closeIdleConnections = () => {
    fallback.closeIdleConnections();
    for (const [socket, state] of sockets) if (!state.busy && !state.fallback) socket.end();
  };
  server.closeAllConnections = () => { for (const socket of sockets.keys()) socket.destroy(); };
  return server;
}
