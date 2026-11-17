import type { IncomingMessage, ServerResponse } from 'node:http';

export class HttpError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

export type BufferedRequest = IncomingMessage & { bufferedBody?: Buffer };
export type BufferedWriter = {
  sendBuffered(status: number, data: string | Buffer, type: string, length: number, id: number, headers?: Record<string, string>): void;
};

let requestId = 0;
export const JSON_TYPE = 'application/json; charset=utf-8';
const responseHeaders = ['content-type', '', 'content-length', '', 'x-github-request-id', ''];
export function json(res: ServerResponse, status: number, body: unknown, headers?: Record<string, string>): void {
  const data = JSON.stringify(body);
  send(res, status, data, JSON_TYPE, headers);
}

export function send(res: ServerResponse, status: number, data: string | Buffer, type: string, headers?: Record<string, string>, length?: number): void {
  if (res.destroyed || res.writableEnded) return;
  const writer = res as ServerResponse & Partial<BufferedWriter>;
  if (writer.sendBuffered) {
    writer.sendBuffered(status, data, type, length ?? Buffer.byteLength(data), ++requestId, headers);
    return;
  }
  responseHeaders[1] = type;
  responseHeaders[3] = String(length ?? Buffer.byteLength(data));
  responseHeaders[5] = (++requestId).toString(16);
  if (headers) res.writeHead(status, {
    'content-type': type, 'content-length': responseHeaders[3], 'x-github-request-id': responseHeaders[5], ...headers,
  });
  else res.writeHead(status, responseHeaders);
  res.end(data);
}

export function authorized(req: IncomingMessage): boolean {
  const token = process.env.MAKEFUN_SHOP_TOKEN;
  return Boolean(token) && req.headers.authorization === `Bearer ${token}`;
}

export function readBody(req: IncomingMessage): Promise<Buffer> {
  return new Promise((resolve, reject) => readBodyCallback(req, (error, body) => error ? reject(error) : resolve(body!)));
}

export function readBodyCallback(req: IncomingMessage, callback: (error: unknown, body?: Buffer) => void): void {
  const buffered = (req as BufferedRequest).bufferedBody;
  if (buffered !== undefined) { callback(null, buffered); return; }
  const parts: Buffer[] = [];
  let size = 0, finished = false;
  const fail = (error: unknown) => {
    if (finished) return;
    finished = true;
    parts.length = 0;
    callback(error);
  };
  req.on('data', (part: Buffer) => {
    if (finished) return;
    size += part.length;
    if (size > 32 * 1024 * 1024) fail(new HttpError(413, 'Request body too large'));
    else parts.push(part);
  });
  req.on('end', () => {
    if (finished) return;
    finished = true;
    callback(null, parts.length === 1 ? parts[0] : Buffer.concat(parts, size));
  });
  req.on('error', fail);
  req.on('aborted', () => fail(new HttpError(400, 'Request aborted')));
}

export function parseJson(data: Buffer): any {
  try {
    return JSON.parse(data.toString('utf8'));
  } catch {
    throw new HttpError(400, 'Malformed JSON body');
  }
}

export function objectBody(data: any): Record<string, any> {
  if (!data || typeof data !== 'object' || Array.isArray(data)) throw new HttpError(400, 'Expected a JSON object');
  return data;
}

export function requiredString(value: any, field: string): string {
  if (typeof value !== 'string' || value.length === 0) throw new HttpError(422, `${field} is required`);
  return value;
}

export function page<T>(items: T[], url: URL, res: ServerResponse): T[] {
  const size = Math.max(1, Math.min(100, Number(url.searchParams.get('per_page')) || 30));
  const index = Math.max(1, Number(url.searchParams.get('page')) || 1);
  if (index * size < items.length) {
    const next = new URL(url);
    next.searchParams.set('per_page', String(size));
    next.searchParams.set('page', String(index + 1));
    res.setHeader('link', `<${next}>; rel="next"`);
  }
  return items.slice((index - 1) * size, index * size);
}
