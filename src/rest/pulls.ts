import type { ServerResponse } from 'node:http';
import { HttpError, json, page } from '../http.ts';
import type { Repo, Store } from '../store.ts';

export function pullRoutes(store: Store, repo: Repo, method: string, tail: string[], url: URL, body: any, res: ServerResponse): boolean {
  if (tail[0] !== 'pulls') return false;
  if (tail.length === 1) {
    if (method === 'POST') json(res, 201, store.pullView(repo, store.createPull(repo, body)));
    else if (method === 'GET') {
      const state = url.searchParams.get('state') ?? 'open';
      const list = [...repo.pulls.values()].filter(p => state === 'all' || p.state === state).sort((a, b) => b.number - a.number);
      json(res, 200, page(list.map(p => store.pullView(repo, p)), url, res));
    } else return false;
    return true;
  }
  const pull = repo.pulls.get(Number(tail[1]));
  if (!pull) throw new HttpError(404, 'Not Found');
  if (tail.length === 2 && method === 'GET') json(res, 200, store.pullView(repo, pull));
  else if (tail.length === 3 && tail[2] === 'files' && method === 'GET') json(res, 200, page(store.pullFiles(repo, pull), url, res));
  else if (tail.length === 3 && tail[2] === 'merge' && method === 'PUT') json(res, 200, store.mergePull(repo, pull, body));
  else return false;
  return true;
}
