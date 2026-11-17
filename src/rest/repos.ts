import type { ServerResponse } from 'node:http';
import { HttpError, json, page, requiredString } from '../http.ts';
import type { Store, Repo } from '../store.ts';
import { isOid } from '../git/objects.ts';
import { contents } from './contents.ts';

export function commitView(repo: Repo, sha: string): any {
  const commit = repo.db.commit(sha);
  const person = (line: string) => {
    const match = /^(.*) <(.*)> (\d+) ([+-]\d{4})$/.exec(line);
    return { name: match?.[1] ?? 'Ethan Moore', email: match?.[2] ?? '', date: new Date(Number(match?.[3] ?? 1767225600) * 1000).toISOString() };
  };
  return { sha, commit: { message: commit.message.replace(/\n$/, ''), author: person(commit.author), committer: person(commit.committer), tree: { sha: commit.tree } }, parents: commit.parents.map(sha => ({ sha })) };
}

export function repoRoutes(store: Store, repo: Repo, method: string, tail: string[], url: URL, body: any, res: ServerResponse): boolean {
  if (!tail.length) {
    if (method === 'GET') json(res, 200, store.repoView(repo));
    else if (method === 'PATCH') {
      if (body.description !== undefined) repo.description = body.description;
      if (body.default_branch !== undefined) {
        repo.default_branch = requiredString(body.default_branch, 'default_branch');
        repo.fetchCache.clear();
      }
      if (body.private !== undefined) repo.private = body.private === true;
      json(res, 200, store.repoView(repo));
    } else if (method === 'DELETE') { store.deleteRepo(repo); json(res, 204, null); }
    else return false;
    return true;
  }
  if (tail[0] === 'contents') { contents(store, repo, method, tail.slice(1).join('/'), url, body, res); return true; }
  if (tail[0] === 'branches' && method === 'GET') {
    if (tail.length > 1) {
      const name = tail.slice(1).join('/'), sha = repo.refs.get(`refs/heads/${name}`);
      if (!sha) throw new HttpError(404, 'Not Found');
      json(res, 200, { name, commit: { sha }, protected: false });
    } else {
      const list = [...repo.refs].filter(([ref]) => ref.startsWith('refs/heads/')).map(([ref, sha]) => ({ name: ref.slice(11), commit: { sha }, protected: false }));
      json(res, 200, page(list, url, res));
    }
    return true;
  }
  if (tail[0] === 'commits' && method === 'GET') {
    const tip = store.resolveRef(repo, tail.length > 1 ? tail.slice(1).join('/') : url.searchParams.get('sha') ?? undefined);
    if (tail.length > 1) {
      if (!tip) throw new HttpError(404, 'Not Found');
      json(res, 200, commitView(repo, repo.db.peel(tip)));
    } else {
      const commits: any[] = [], queue = tip ? [repo.db.peel(tip)] : [], seen = new Set<string>();
      for (let i = 0; i < queue.length; i++) {
        const oid = queue[i];
        if (seen.has(oid)) continue;
        seen.add(oid);
        commits.push(commitView(repo, oid));
        queue.push(...repo.db.commit(oid).parents);
      }
      json(res, 200, page(commits, url, res));
    }
    return true;
  }
  if (tail[0] === 'git' && ['refs', 'ref'].includes(tail[1])) {
    const ref = tail.slice(2).join('/');
    if (method === 'POST' && !ref) {
      const fullRef = requiredString(body.ref, 'ref'), sha = requiredString(body.sha, 'sha');
      if (!fullRef.startsWith('refs/') || !isOid(sha) || !repo.db.objects.has(sha)) throw new HttpError(422, 'Invalid reference');
      if (repo.refs.has(fullRef)) throw new HttpError(422, 'Reference already exists');
      store.updateRef(repo, fullRef, sha);
      json(res, 201, { ref: fullRef, object: { type: repo.db.get(sha).type, sha } });
    } else if (method === 'GET' && ref) {
      const fullRef = ref.startsWith('refs/') ? ref : `refs/${ref}`;
      const sha = repo.refs.get(fullRef);
      if (!sha) throw new HttpError(404, 'Not Found');
      json(res, 200, { ref: fullRef, object: { type: repo.db.get(sha).type, sha } });
    } else if (method === 'DELETE' && ref) {
      if (!repo.refs.delete(`refs/${ref}`)) throw new HttpError(404, 'Not Found');
      repo.fetchCache.clear(); json(res, 204, null);
    } else return false;
    return true;
  }
  return false;
}
