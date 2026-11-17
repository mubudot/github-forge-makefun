import { ZERO, isOid } from './objects.ts';
import { readPack } from './pack.ts';
import { parsePackets, pkt, FLUSH, sideband } from './pktline.ts';
import type { Repo } from '../store.ts';

const reportCache = new Map<string, Buffer>();

export function receivePack(repo: Repo, body: Buffer): Buffer {
  const { lines, offset } = parsePackets(body, true);
  const capabilities = lines[0]?.split('\0')[1] ?? '';
  const commands = lines.map(line => {
    const [oldOid, newOid, ref] = line.split('\0')[0].trim().split(' ');
    if (!isOid(oldOid) || !isOid(newOid) || !/^refs\/(heads|tags)\/[^\s\0]+$/.test(ref ?? '')) throw new Error('Invalid ref command');
    return { oldOid, newOid, ref };
  });
  let unpack = 'ok';
  try {
    if (offset < body.length) readPack(body.subarray(offset), repo.db);
  } catch {
    unpack = 'invalid pack';
  }
  const reports = [`unpack ${unpack}\n`];
  for (const { oldOid, newOid, ref } of commands) {
    const current = repo.refs.get(ref) ?? ZERO;
    let reason = unpack === 'ok' ? '' : 'unpacker error';
    if (!reason && current !== oldOid) reason = 'stale info';
    if (!reason && newOid !== ZERO && !repo.db.objects.has(newOid)) reason = 'missing object';
    if (!reason && current !== ZERO && newOid !== ZERO && !repo.db.isAncestor(current, newOid)) reason = 'non-fast-forward';
    if (reason) reports.push(`ng ${ref} ${reason}\n`);
    else {
      if (newOid === ZERO) repo.refs.delete(ref); else repo.refs.set(ref, newOid);
      repo.fetchCache.clear();
      reports.push(`ok ${ref}\n`);
    }
  }
  const banded = capabilities.includes('side-band-64k');
  const key = `${banded ? 1 : 0}:${reports.join('')}`;
  let result = reportCache.get(key);
  if (!result) {
    const payload = Buffer.concat([...reports.map(pkt), FLUSH]);
    result = banded ? Buffer.concat(sideband(payload)) : payload;
    if (reportCache.size >= 128) reportCache.delete(reportCache.keys().next().value!);
    reportCache.set(key, result);
  }
  return result;
}
