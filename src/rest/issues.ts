import type { ServerResponse } from 'node:http';
import { HttpError, json, page, send } from '../http.ts';
import type { Repo, Store } from '../store.ts';

export function issueRoutes(store: Store, repo: Repo, method: string, tail: string[], url: URL, body: any, res: ServerResponse): boolean {
  if (tail[0] === 'labels') {
    if (tail.length === 1 && method === 'POST') json(res, 201, store.createLabel(repo, body));
    else if (tail.length === 1 && method === 'GET') json(res, 200, page([...repo.labels.values()], url, res));
    else return false;
    return true;
  }
  if (tail[0] !== 'issues') return false;
  if (tail.length === 1) {
    if (method === 'POST') json(res, 201, store.issueView(store.createIssue(repo, body)));
    else if (method === 'GET') json(res, 200, page(store.listIssues(repo, Object.fromEntries(url.searchParams)).map(i => store.issueView(i)), url, res));
    else return false;
    return true;
  }
  const issue = repo.issues.get(Number(tail[1]));
  if (!issue) throw new HttpError(404, 'Not Found');
  if (tail.length === 2) {
    if (method === 'GET') {
      const body = store.issueJson(issue);
      send(res, 200, body, 'application/json; charset=utf-8', undefined, issue.serializedBytes);
    } else if (method === 'PATCH') json(res, 200, store.issueView(store.patchIssue(repo, issue, body)));
    else return false;
    return true;
  }
  if (tail[2] === 'comments' && tail.length === 3) {
    if (method === 'GET') json(res, 200, page(issue.comments, url, res));
    else if (method === 'POST') json(res, 201, store.comment(repo, issue, body.body));
    else return false;
    return true;
  }
  return false;
}
