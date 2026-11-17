import { ZERO } from './objects.ts';
import { writePack } from './pack.ts';
import { pkt, FLUSH, DELIM, sideband, parsePackets } from './pktline.ts';
import type { Repo } from '../store.ts';

// SHA-1 is the protocol default; optional agent and object-format negotiation
// would add capability parsing and request packets to every exchange.
const V2 = Buffer.concat(['version 2\n', 'ls-refs=unborn\n', 'fetch=shallow\n'].map(pkt).concat(FLUSH));

export function advertise(repo: Repo, service: string, v2: boolean): Buffer {
  if (service === 'git-upload-pack' && v2) return V2;
  const head = `refs/heads/${repo.default_branch}`;
  const caps = service === 'git-receive-pack' ? 'report-status side-band-64k delete-refs' : `side-band-64k shallow symref=HEAD:${head}`;
  const refs = [...repo.refs].sort(([a], [b]) => a.localeCompare(b));
  if (service === 'git-upload-pack' && repo.refs.has(head)) refs.unshift(['HEAD', repo.refs.get(head)!]);
  const parts = [pkt(`# service=${service}\n`), FLUSH];
  if (refs.length === 0) parts.push(pkt(`${ZERO} capabilities^{}\0${caps}\n`));
  refs.forEach(([ref, oid], index) => {
    parts.push(pkt(`${oid} ${ref}${index === 0 ? `\0${caps}` : ''}\n`));
    if (service === 'git-upload-pack' && repo.db.get(oid).type === 'tag') parts.push(pkt(`${repo.db.peel(oid)} ${ref}^{}\n`));
  });
  parts.push(FLUSH);
  return Buffer.concat(parts);
}

function lsRefs(repo: Repo, lines: string[]): Buffer {
  const prefixes = lines.filter(l => l.startsWith('ref-prefix ')).map(l => l.slice(11));
  const keep = (name: string) => !prefixes.length || prefixes.some(p => name.startsWith(p));
  const parts: Buffer[] = [];
  const head = `refs/heads/${repo.default_branch}`;
  const tip = repo.refs.get(head);
  const symref = lines.includes('symrefs') ? ` symref-target:${head}` : '';
  if (keep('HEAD')) {
    if (tip) parts.push(pkt(`${tip} HEAD${symref}\n`));
    else if (lines.includes('unborn')) parts.push(pkt(`unborn HEAD${symref}\n`));
  }
  for (const [name, oid] of [...repo.refs].sort(([a], [b]) => a.localeCompare(b))) {
    if (!keep(name)) continue;
    const peeled = lines.includes('peel') && repo.db.get(oid).type === 'tag' ? ` peeled:${repo.db.peel(oid)}` : '';
    parts.push(pkt(`${oid} ${name}${peeled}\n`));
  }
  return Buffer.concat([...parts, FLUSH]);
}

export function uploadPack(repo: Repo, body: Buffer, v2: boolean): Buffer {
  const cacheKey = `${v2 ? 2 : 0}:${body.toString()}`;
  const cached = repo.fetchCache.get(cacheKey);
  if (cached) return cached;
  const { lines } = parsePackets(body);
  if (lines.includes('command=ls-refs')) {
    const response = lsRefs(repo, lines);
    if (repo.fetchCache.size >= 16) repo.fetchCache.delete(repo.fetchCache.keys().next().value!);
    repo.fetchCache.set(cacheKey, response);
    return response;
  }
  const wants = [...new Set(lines.filter(l => l.startsWith('want ')).map(l => l.split(' ')[1]))];
  const haves = lines.filter(l => l.startsWith('have ')).map(l => l.slice(5));
  const shallow = lines.filter(l => l.startsWith('shallow ')).map(l => l.slice(8));
  const depth = Math.max(0, Number(lines.find(l => l.startsWith('deepen '))?.slice(7)) || 0);
  if (depth > 100000) throw new Error('Invalid fetch depth');
  for (const oid of wants) if (!repo.db.objects.has(oid)) throw new Error('Unknown wanted object');
  const held = repo.db.reachable(haves, { shallow: new Set(shallow) });
  const wanted = repo.db.reachable(wants, { depth });
  const boundary = new Set<string>();
  if (depth) {
    const queue: [string, number][] = wants.map(oid => [repo.db.peel(oid), 1]);
    const levels = new Map<string, number>();
    for (let i = 0; i < queue.length; i++) {
      const [oid, level] = queue[i];
      if ((levels.get(oid) ?? Infinity) <= level) continue;
      levels.set(oid, level);
      if (repo.db.get(oid).type !== 'commit') continue;
      const parents = repo.db.commit(oid).parents;
      if (level === depth && parents.length) boundary.add(oid);
      else for (const parent of parents) queue.push([parent, level + 1]);
    }
    for (const oid of boundary) if ((levels.get(oid) ?? depth) < depth) boundary.delete(oid);
  }
  const info: Buffer[] = [...boundary].map(oid => pkt(`shallow ${oid}\n`));
  for (const oid of shallow) if (wanted.has(oid) && !boundary.has(oid)) info.push(pkt(`unshallow ${oid}\n`));
  const objects = [...wanted].filter(oid => !held.has(oid)).map(oid => repo.db.get(oid));
  const pack = writePack(objects);
  const parts: Buffer[] = [];
  if (v2) {
    if (info.length) parts.push(pkt('shallow-info\n'), ...info, DELIM);
    parts.push(pkt('packfile\n'), ...sideband(pack));
  } else {
    if (info.length) parts.push(...info, FLUSH);
    parts.push(pkt('NAK\n'), ...sideband(pack));
  }
  const response = Buffer.concat(parts);
  if (repo.fetchCache.size >= 16) repo.fetchCache.delete(repo.fetchCache.keys().next().value!);
  repo.fetchCache.set(cacheKey, response);
  return response;
}
