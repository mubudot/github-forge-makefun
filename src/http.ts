import type { IncomingMessage, ServerResponse } from 'node:http';

export class HttpError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

let requestId = 0;
export function json(res: ServerResponse, status: number, body: unknown, headers: Record<string, string> = {}): void {
  const data = JSON.stringify(body);
  send(res, status, data, 'application/json; charset=utf-8', headers);
}

export function send(res: ServerResponse, status: number, data: string | Buffer, type: string, headers: Record<string, string> = {}): void {
  if (res.destroyed || res.writableEnded) return;
  res.writeHead(status, {
    'content-type': type,
    'content-length': Buffer.byteLength(data),
    'x-github-request-id': (++requestId).toString(16),
    ...headers,
  });
  res.end(data);
}

export function authorized(req: IncomingMessage): boolean {
  const token = process.env.MAKEFUN_SHOP_TOKEN;
  return Boolean(token) && req.headers.authorization === `Bearer ${token}`;
}

export function readBody(req: IncomingMessage): Promise<Buffer> {
  return new Promise((resolve, reject) => {
    const parts: Buffer[] = [];
    let size = 0;
    req.on('data', (part: Buffer) => {
      size += part.length;
      if (size > 32 * 1024 * 1024) reject(new HttpError(413, 'Request body too large'));
      else parts.push(part);
    });
    req.on('end', () => resolve(parts.length === 1 ? parts[0] : Buffer.concat(parts, size)));
    req.on('error', reject);
    req.on('aborted', () => reject(new HttpError(400, 'Request aborted')));
  });
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
