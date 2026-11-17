import type { IncomingMessage, ServerResponse } from 'node:http';
import { HttpError, JSON_TYPE, json, objectBody, parseJson, readBodyCallback, send } from './http.ts';
import type { Store } from './store.ts';
import { advertise, uploadPack } from './git/upload-pack.ts';
import { receivePack } from './git/receive-pack.ts';

type Route = { owner: string; name: string; kind: 'issue' | 'git'; number?: number; service?: string; advertisement?: boolean };

/** Cache only routing data: repository identity and issue state are resolved on every request. */
export function fastRouter(store: Store): (req: IncomingMessage, res: ServerResponse) => boolean {
  const routes = new Map<string, Route | null>();
  const expected = process.env.MAKEFUN_SHOP_TOKEN ? `Bearer ${process.env.MAKEFUN_SHOP_TOKEN}` : '';
  const error = (res: ServerResponse, err: unknown) => json(res, err instanceof HttpError ? err.status : 400,
    { message: err instanceof HttpError ? err.message : 'Invalid request' });
  const header = (req: IncomingMessage, name: string): string | undefined => {
    const raw = req.rawHeaders;
    for (let i = 0; i < raw.length; i += 2) if (raw[i].toLowerCase() === name) return raw[i + 1];
  };
  return (req, res) => {
    const key = req.url ?? '/';
    let route = routes.get(key);
    if (route === undefined) {
      route = null;
      const url = new URL(key, 'http://forge');
      const issue = /^\/api\/v3\/repos\/([^/]+)\/([^/]+)\/issues(?:\/(\d+))?\/?$/.exec(url.pathname);
      const git = /^\/([^/]+)\/([^/]+)\.git\/(info\/refs|git-upload-pack|git-receive-pack)$/.exec(url.pathname);
      if (issue) route = { owner: decodeURIComponent(issue[1]), name: decodeURIComponent(issue[2]), kind: 'issue', number: issue[3] === undefined ? undefined : Number(issue[3]) };
      else if (git) route = { owner: decodeURIComponent(git[1]), name: decodeURIComponent(git[2]), kind: 'git',
        service: git[3] === 'info/refs' ? url.searchParams.get('service') ?? '' : git[3], advertisement: git[3] === 'info/refs' };
      if (routes.size >= 512) routes.delete(routes.keys().next().value!);
      routes.set(key, route);
    }
    if (!route) return false;
    const method = req.method;
    if (route.kind === 'issue' && !((route.number !== undefined && method === 'GET') || (route.number === undefined && method === 'POST'))) return false;
    if (route.kind === 'git' && method !== (route.advertisement ? 'GET' : 'POST')) return false;
    if (!expected || header(req, 'authorization') !== expected) { json(res, 401, { message: 'Bad credentials' }); return true; }
    try {
      const repo = store.requireRepo(route.owner, route.name);
      if (route.kind === 'issue') {
        if (route.number !== undefined) {
          const issue = repo.issues.get(route.number);
          if (!issue) throw new HttpError(404, 'Not Found');
          const body = store.issueJson(issue);
          send(res, 200, body, JSON_TYPE, undefined, issue.serializedBytes);
        } else {
          readBodyCallback(req, (err, bytes) => {
            if (err) { error(res, err); return; }
            try {
              const issue = store.createIssue(repo, objectBody(bytes!.length ? parseJson(bytes!) : {}));
              json(res, 201, store.issueView(issue));
            } catch (err) { error(res, err); }
          });
        }
      } else {
        const service = route.service!;
        if (service !== 'git-upload-pack' && service !== 'git-receive-pack') throw new HttpError(400, 'Invalid Git service');
        const v2 = header(req, 'git-protocol') === 'version=2';
        if (route.advertisement) send(res, 200, advertise(repo, service, v2), `application/x-${service}-advertisement`);
        else readBodyCallback(req, (err, bytes) => {
          if (err) { error(res, err); return; }
          try { send(res, 200, service === 'git-upload-pack' ? uploadPack(repo, bytes!, v2) : receivePack(repo, bytes!), `application/x-${service}-result`); }
          catch { error(res, new HttpError(400, 'Malformed Git request')); }
        });
      }
    } catch (err) { error(res, err); }
    return true;
  };
}
