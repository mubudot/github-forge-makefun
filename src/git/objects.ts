import { createHash } from 'node:crypto';

export type ObjectType = 'commit' | 'tree' | 'blob' | 'tag';
export type GitObject = { type: ObjectType; data: Buffer; packed?: Buffer };
export type TreeEntry = { mode: string; name: string; oid: string };
export type Commit = { tree: string; parents: string[]; author: string; committer: string; message: string };
export const ZERO = '0'.repeat(40);
export const isOid = (s: string): boolean => /^[0-9a-f]{40}$/.test(s);

export function sha1(data: Buffer): Buffer {
  return createHash('sha1').update(data).digest();
}

export function hashObject(type: ObjectType, data: Buffer): string {
  return createHash('sha1').update(`${type} ${data.length}\0`).update(data).digest('hex');
}

export function parseCommit(data: Buffer): Commit {
  const text = data.toString('utf8');
  const at = text.indexOf('\n\n');
  const result: Commit = { tree: '', parents: [], author: '', committer: '', message: at < 0 ? '' : text.slice(at + 2) };
  for (const line of text.slice(0, at < 0 ? text.length : at).split('\n')) {
    const space = line.indexOf(' ');
    const key = line.slice(0, space), value = line.slice(space + 1);
    if (key === 'tree') result.tree = value;
    if (key === 'parent') result.parents.push(value);
    if (key === 'author') result.author = value;
    if (key === 'committer') result.committer = value;
  }
  if (!isOid(result.tree)) throw new Error('Invalid commit tree');
  return result;
}

export function parseTree(data: Buffer): TreeEntry[] {
  const entries: TreeEntry[] = [];
  let offset = 0;
  while (offset < data.length) {
    const space = data.indexOf(32, offset), nul = data.indexOf(0, space + 1);
    if (space < offset || nul < space || nul + 21 > data.length) throw new Error('Invalid tree');
    entries.push({ mode: data.toString('ascii', offset, space), name: data.toString('utf8', space + 1, nul), oid: data.toString('hex', nul + 1, nul + 21) });
    offset = nul + 21;
  }
  return entries;
}

export class ObjectDatabase {
  objects = new Map<string, GitObject>();
  commits = new Map<string, Commit>();
  trees = new Map<string, TreeEntry[]>();

  put(type: ObjectType, data: Buffer, packed?: Buffer): string {
    const oid = hashObject(type, data);
    if (!this.objects.has(oid)) this.objects.set(oid, { type, data, packed });
    return oid;
  }

  get(oid: string): GitObject {
    const object = this.objects.get(oid);
    if (!object) throw new Error('Missing Git object');
    return object;
  }

  commit(oid: string): Commit {
    let value = this.commits.get(oid);
    if (!value) { value = parseCommit(this.get(oid).data); this.commits.set(oid, value); }
    return value;
  }

  tree(oid: string): TreeEntry[] {
    let value = this.trees.get(oid);
    if (!value) { value = parseTree(this.get(oid).data); this.trees.set(oid, value); }
    return value;
  }

  peel(oid: string): string {
    const seen = new Set<string>();
    while (this.get(oid).type === 'tag') {
      if (seen.has(oid)) throw new Error('Tag cycle');
      seen.add(oid);
      oid = /^object ([0-9a-f]{40})/m.exec(this.get(oid).data.toString())?.[1] ?? '';
    }
    return oid;
  }

  writeTree(entries: TreeEntry[]): string {
    const sorted = [...entries].sort((a, b) => Buffer.compare(Buffer.from(a.name + (a.mode === '40000' ? '/' : '')), Buffer.from(b.name + (b.mode === '40000' ? '/' : ''))));
    return this.put('tree', Buffer.concat(sorted.flatMap(e => [Buffer.from(`${e.mode} ${e.name}\0`), Buffer.from(e.oid, 'hex')])));
  }

  writeCommit(tree: string, parents: string[], message: string, epoch: number): string {
    const identity = `Ethan Moore <mubudot@users.noreply.github.com> ${epoch} +0000`;
    return this.put('commit', Buffer.from(`tree ${tree}\n${parents.map(p => `parent ${p}\n`).join('')}author ${identity}\ncommitter ${identity}\n\n${message.replace(/\n?$/, '\n')}`));
  }

  files(commit: string): Map<string, TreeEntry> {
    const result = new Map<string, TreeEntry>();
    if (!commit) return result;
    const visit = (tree: string, prefix: string) => {
      for (const entry of this.tree(tree)) {
        if (entry.mode === '40000' || entry.mode === '040000') visit(entry.oid, prefix + entry.name + '/');
        else result.set(prefix + entry.name, entry);
      }
    };
    visit(this.commit(this.peel(commit)).tree, '');
    return result;
  }

  treeFromFiles(files: Map<string, TreeEntry>): string {
    type Dir = { entries: TreeEntry[]; children: Map<string, Dir> };
    const root: Dir = { entries: [], children: new Map() };
    for (const [path, entry] of files) {
      const parts = path.split('/');
      let dir = root;
      for (const part of parts.slice(0, -1)) {
        if (!dir.children.has(part)) dir.children.set(part, { entries: [], children: new Map() });
        dir = dir.children.get(part)!;
      }
      dir.entries.push({ ...entry, name: parts.at(-1)! });
    }
    const write = (dir: Dir): string => this.writeTree([...dir.entries, ...[...dir.children].map(([name, child]) => ({ mode: '40000', name, oid: write(child) }))]);
    return write(root);
  }

  isAncestor(ancestor: string, tip: string): boolean {
    const todo = [tip], seen = new Set<string>();
    while (todo.length) {
      const oid = todo.pop()!;
      if (oid === ancestor) return true;
      if (seen.has(oid)) continue;
      seen.add(oid);
      const object = this.objects.get(oid);
      if (object?.type === 'commit') todo.push(...this.commit(oid).parents);
      else if (object?.type === 'tag') todo.push(this.peel(oid));
    }
    return false;
  }

  mergeBase(a: string, b: string): string {
    const ancestors = new Set<string>(), todo = [a];
    while (todo.length) {
      const oid = todo.pop()!;
      if (ancestors.has(oid)) continue;
      ancestors.add(oid);
      todo.push(...this.commit(oid).parents);
    }
    const queue = [b], seen = new Set<string>();
    for (let i = 0; i < queue.length; i++) {
      const oid = queue[i];
      if (ancestors.has(oid)) return oid;
      if (seen.has(oid)) continue;
      seen.add(oid);
      queue.push(...this.commit(oid).parents);
    }
    return '';
  }

  reachable(roots: string[], options: { depth?: number; shallow?: Set<string> } = {}): Set<string> {
    const seen = new Set<string>(), bestDepth = new Map<string, number>();
    const todo: [string, number][] = roots.map(oid => [oid, 1]);
    while (todo.length) {
      const [oid, level] = todo.pop()!;
      const object = this.objects.get(oid);
      if (!object) continue;
      if (seen.has(oid) && (object.type !== 'commit' || (bestDepth.get(oid) ?? 0) <= level)) continue;
      seen.add(oid);
      if (object.type === 'commit') {
        bestDepth.set(oid, level);
        const commit = this.commit(oid);
        todo.push([commit.tree, level]);
        if ((!options.depth || level < options.depth) && !options.shallow?.has(oid)) {
          for (const parent of commit.parents) todo.push([parent, level + 1]);
        }
      } else if (object.type === 'tree') {
        for (const entry of this.tree(oid)) if (entry.mode !== '160000') todo.push([entry.oid, level]);
      } else if (object.type === 'tag') todo.push([this.peel(oid), level]);
    }
    return seen;
  }
}
