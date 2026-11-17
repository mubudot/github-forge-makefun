import type { ServerResponse } from 'node:http';
import { HttpError, json, requiredString } from '../http.ts';
import type { Store, Repo } from '../store.ts';
import type { TreeEntry } from '../git/objects.ts';

function contentView(repo: Repo, path: string, entry: TreeEntry): any {
  const dir = entry.mode === '40000' || entry.mode === '040000';
  const object = repo.db.get(entry.oid);
  return { type: dir ? 'dir' : 'file', name: path.split('/').at(-1), path, sha: entry.oid,
    size: dir ? 0 : object.data.length, ...(dir ? {} : { encoding: 'base64', content: object.data.toString('base64') }) };
}

export function contents(store: Store, repo: Repo, method: string, path: string, url: URL, body: any, res: ServerResponse): void {
  const branch = body.branch ?? repo.default_branch;
  const tip = store.resolveRef(repo, method === 'GET' ? url.searchParams.get('ref') ?? undefined : branch);
  if (method === 'GET') {
    if (!tip) throw new HttpError(404, 'Not Found');
    let tree = repo.db.commit(repo.db.peel(tip)).tree;
    const parts = path.split('/').filter(Boolean);
    if (!parts.length) {
      json(res, 200, repo.db.tree(tree).map(e => contentView(repo, e.name, e))); return;
    }
    for (let i = 0; i < parts.length; i++) {
      const entry = repo.db.tree(tree).find(e => e.name === parts[i]);
      if (!entry) throw new HttpError(404, 'Not Found');
      if (i === parts.length - 1) {
        if (entry.mode === '40000' || entry.mode === '040000') json(res, 200, repo.db.tree(entry.oid).map(e => contentView(repo, `${path}/${e.name}`, e)));
        else json(res, 200, contentView(repo, path, entry));
        return;
      }
      if (entry.mode !== '40000' && entry.mode !== '040000') throw new HttpError(404, 'Not Found');
      tree = entry.oid;
    }
  }
  if (method !== 'PUT' && method !== 'DELETE') throw new HttpError(404, 'Not Found');
  if (!path || path.split('/').some(p => !p || p === '.' || p === '..' || p.includes('\0'))) throw new HttpError(422, 'Invalid path');
  if (body.branch && !tip) throw new HttpError(404, 'Branch not found');
  const files = repo.db.files(tip), old = files.get(path);
  if (method === 'DELETE' && !old) throw new HttpError(404, 'Not Found');
  if ((old && body.sha !== old.oid) || (!old && body.sha)) throw new HttpError(409, 'Content SHA does not match');
  let entry: TreeEntry | undefined;
  if (method === 'DELETE') files.delete(path);
  else {
    if (typeof body.content !== 'string') throw new HttpError(422, 'content is required');
    const data = Buffer.from(body.content.replace(/\s/g, ''), 'base64');
    entry = { mode: old?.mode ?? '100644', name: path.split('/').at(-1)!, oid: repo.db.put('blob', data) };
    files.set(path, entry);
  }
  const tree = repo.db.treeFromFiles(files);
  const message = requiredString(body.message, 'message');
  const sha = repo.db.writeCommit(tree, tip ? [tip] : [], message, store.epoch);
  store.updateRef(repo, `refs/heads/${branch}`, sha);
  json(res, method === 'DELETE' || old ? 200 : 201, { content: entry ? contentView(repo, path, entry) : null, commit: { sha, message, tree: { sha: tree }, parents: tip ? [{ sha: tip }] : [] } });
}
