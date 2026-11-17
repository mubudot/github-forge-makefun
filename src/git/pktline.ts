export const FLUSH = Buffer.from('0000');
export const DELIM = Buffer.from('0001');

export function pkt(data: string | Buffer): Buffer {
  const payload = typeof data === 'string' ? Buffer.from(data) : data;
  if (payload.length > 65516) throw new Error('Pkt-line too large');
  return Buffer.concat([Buffer.from((payload.length + 4).toString(16).padStart(4, '0')), payload]);
}

export function parsePackets(data: Buffer, stopAtFlush = false): { lines: string[]; offset: number } {
  const lines: string[] = [];
  let offset = 0;
  while (offset < data.length) {
    const header = data.toString('ascii', offset, offset + 4);
    if (!/^[0-9a-fA-F]{4}$/.test(header)) throw new Error('Invalid pkt-line');
    const size = parseInt(header, 16);
    offset += 4;
    if (size === 0) { if (stopAtFlush) break; else continue; }
    if (size === 1 || size === 2) continue;
    if (size < 4 || size > 65520 || offset + size - 4 > data.length) throw new Error('Invalid pkt-line size');
    lines.push(data.toString('utf8', offset, offset + size - 4).replace(/\n$/, ''));
    offset += size - 4;
  }
  return { lines, offset };
}

export function sideband(data: Buffer): Buffer[] {
  const parts: Buffer[] = [];
  for (let i = 0; i < data.length; i += 65515) parts.push(pkt(Buffer.concat([Buffer.from([1]), data.subarray(i, i + 65515)])));
  parts.push(FLUSH);
  return parts;
}
