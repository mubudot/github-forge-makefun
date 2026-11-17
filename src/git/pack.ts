import { deflateSync, inflateSync } from 'node:zlib';
import { hashObject, sha1, type GitObject, type ObjectDatabase, type ObjectType } from './objects.ts';

const types: Record<number, ObjectType> = { 1: 'commit', 2: 'tree', 3: 'blob', 4: 'tag' };
const codes = { commit: 1, tree: 2, blob: 3, tag: 4 };
const MAX_OBJECT = 16 * 1024 * 1024;

function entryHeader(code: number, size: number): Buffer {
  const out: number[] = [];
  let byte = (code << 4) | (size & 15);
  size = Math.floor(size / 16);
  while (size) {
    out.push(byte | 128);
    byte = size & 127;
    size = Math.floor(size / 128);
  }
  out.push(byte);
  return Buffer.from(out);
}

function applyDelta(base: Buffer, data: Buffer): Buffer {
  let pos = 0;
  const size = () => {
    let value = 0, shift = 0, byte: number;
    do {
      if (pos >= data.length || shift > 28) throw new Error('Invalid delta size');
      byte = data[pos++];
      value += (byte & 127) * 2 ** shift;
      shift += 7;
    } while (byte & 128);
    return value;
  };
  if (size() !== base.length) throw new Error('Delta base size mismatch');
  const length = size();
  if (length > MAX_OBJECT) throw new Error('Delta too large');
  const out = Buffer.allocUnsafe(length);
  let written = 0;
  while (pos < data.length) {
    const op = data[pos++];
    if (op & 128) {
      let offset = 0, count = 0;
      for (let bit = 0; bit < 4; bit++) if (op & (1 << bit)) offset += data[pos++] * 2 ** (8 * bit);
      for (let bit = 4; bit < 7; bit++) if (op & (1 << bit)) count += data[pos++] * 2 ** (8 * (bit - 4));
      if (count === 0) count = 65536;
      if (!Number.isFinite(offset + count) || offset + count > base.length || written + count > length) throw new Error('Invalid delta copy');
      base.copy(out, written, offset, offset + count);
      written += count;
    } else {
      if (!op || pos + op > data.length || written + op > length) throw new Error('Invalid delta insert');
      data.copy(out, written, pos, pos + op);
      pos += op;
      written += op;
    }
  }
  if (written !== length) throw new Error('Delta length mismatch');
  return out;
}

/** Decode and resolve a whole pack before making any of its objects visible. */
export function readPack(pack: Buffer, db: ObjectDatabase): void {
  if (pack.length < 32 || pack.toString('ascii', 0, 4) !== 'PACK') throw new Error('Invalid pack header');
  if (![2, 3].includes(pack.readUInt32BE(4))) throw new Error('Unsupported pack version');
  const end = pack.length - 20;
  if (!sha1(pack.subarray(0, end)).equals(pack.subarray(end))) throw new Error('Pack checksum mismatch');
  const count = pack.readUInt32BE(8);
  if (count > 200000) throw new Error('Too many pack objects');
  type Entry = { offset: number; code: number; data: Buffer; baseOffset?: number; baseOid?: string; object?: GitObject; oid?: string; packed?: Buffer };
  const entries: Entry[] = [], byOffset = new Map<number, Entry>();
  const resolved = new Map<string, GitObject>();
  let pos = 12, expandedBytes = 0;
  for (let i = 0; i < count; i++) {
    const offset = pos;
    let byte = pack[pos++], size = byte & 15, shift = 4;
    const code = (byte >> 4) & 7;
    while (byte & 128) {
      if (pos >= end || shift > 28) throw new Error('Invalid pack object size');
      byte = pack[pos++];
      size += (byte & 127) * 2 ** shift;
      shift += 7;
    }
    if (size > MAX_OBJECT) throw new Error('Pack object too large');
    expandedBytes += size;
    if (expandedBytes > 64 * 1024 * 1024) throw new Error('Pack expands beyond the memory limit');
    let baseOffset: number | undefined, baseOid: string | undefined;
    if (code === 6) {
      byte = pack[pos++];
      let distance = byte & 127;
      while (byte & 128) {
        if (pos >= end) throw new Error('Invalid delta offset');
        byte = pack[pos++];
        distance = (distance + 1) * 128 + (byte & 127);
      }
      baseOffset = offset - distance;
      if (distance <= 0 || baseOffset < 12) throw new Error('Invalid delta base');
    } else if (code === 7) {
      if (pos + 20 > end) throw new Error('Truncated delta');
      baseOid = pack.toString('hex', pos, pos + 20);
      pos += 20;
    } else if (!types[code]) throw new Error('Invalid pack object type');
    const result = inflateSync(pack.subarray(pos, end), { info: true, maxOutputLength: Math.max(1, size), chunkSize: Math.max(64, Math.min(size, 65536)) }) as unknown as { buffer: Buffer; engine: { bytesWritten: number } };
    pos += result.engine.bytesWritten;
    if (result.buffer.length !== size) throw new Error('Pack object size mismatch');
    const entry: Entry = { offset, code, data: result.buffer, baseOffset, baseOid };
    if (types[code]) {
      entry.object = { type: types[code], data: entry.data, packed: Buffer.from(pack.subarray(offset, pos)) };
      entry.oid = hashObject(entry.object.type, entry.data);
      resolved.set(entry.oid, entry.object);
    }
    entries.push(entry);
    byOffset.set(offset, entry);
  }
  if (pos !== end) throw new Error('Trailing pack data');
  let pending = entries.filter(e => !e.object);
  while (pending.length) {
    const next: Entry[] = [];
    for (const entry of pending) {
      const base = entry.code === 6 ? byOffset.get(entry.baseOffset!)?.object : resolved.get(entry.baseOid!) ?? db.objects.get(entry.baseOid!);
      if (!base) { next.push(entry); continue; }
      entry.object = { type: base.type, data: applyDelta(base.data, entry.data) };
      expandedBytes += entry.object.data.length;
      if (expandedBytes > 64 * 1024 * 1024) throw new Error('Pack expands beyond the memory limit');
      entry.oid = hashObject(base.type, entry.object.data);
      resolved.set(entry.oid, entry.object);
    }
    if (next.length === pending.length) throw new Error('Unresolved pack delta');
    pending = next;
  }
  for (const [oid, object] of resolved) if (!db.objects.has(oid)) db.objects.set(oid, object);
}

export function writePack(objects: GitObject[]): Buffer {
  const header = Buffer.allocUnsafe(12);
  header.write('PACK'); header.writeUInt32BE(2, 4); header.writeUInt32BE(objects.length, 8);
  const chunks: Buffer[] = [header];
  for (const object of objects) {
    object.packed ??= Buffer.concat([entryHeader(codes[object.type], object.data.length), deflateSync(object.data, { level: 1 })]);
    chunks.push(object.packed);
  }
  const body = Buffer.concat(chunks);
  return Buffer.concat([body, sha1(body)]);
}
