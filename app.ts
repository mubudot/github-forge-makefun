import { createServer, type IncomingMessage, type Server, type ServerResponse } from 'node:http';
import { HttpError, authorized, json, send, readBody, parseJson, objectBody, page } from './src/http.ts';
import { Store } from './src/store.ts';
import { advertise, uploadPack } from './src/git/upload-pack.ts';
import { receivePack } from './src/git/receive-pack.ts';
import { repoRoutes } from './src/rest/repos.ts';
import { issueRoutes } from './src/rest/issues.ts';
import { pullRoutes } from './src/rest/pulls.ts';
import { execute } from './src/graphql/executor.ts';
import { mcp, rpcError } from './src/mcp.ts';

type Route = { url: URL; parts: string[]; git: RegExpExecArray | null };

async function dispatch(store: Store, routes: Map<string, Route>, req: IncomingMessage, res: ServerResponse): Promise<void> {
  const key = `${req.headers.host}:${req.url}`;
  let route = routes.get(key);
  if (!route) {
    const url = new URL(req.url ?? '/', `http://${req.headers.host ?? 'localhost'}`);
    route = { url, parts: url.pathname.split('/').filter(Boolean).map(decodeURIComponent),
      git: /^\/([^/]+)\/([^/]+)\.git\/(info\/refs|git-upload-pack|git-receive-pack)$/.exec(url.pathname) };
    if (routes.size >= 512) routes.delete(routes.keys().next().value!);
    routes.set(key, route);
  }
  const { url, parts, git } = route;
  const path = url.pathname;
  const method = req.method ?? 'GET';
  if (path === '/healthz' && method === 'GET') { json(res, 200, { ok: true }); return; }
  if (!authorized(req)) throw new HttpError(401, 'Bad credentials');
  if (git) {
    const repo = store.requireRepo(decodeURIComponent(git[1]), decodeURIComponent(git[2]));
    const v2 = req.headers['git-protocol'] === 'version=2';
    if (method === 'GET' && git[3] === 'info/refs') {
      const service = url.searchParams.get('service') ?? '';
      if (!['git-upload-pack', 'git-receive-pack'].includes(service)) throw new HttpError(400, 'Invalid Git service');
      send(res, 200, advertise(repo, service, v2), `application/x-${service}-advertisement`);
    } else if (method === 'POST' && git[3] !== 'info/refs') {
      const body = await readBody(req);
      try {
        const reply = git[3] === 'git-upload-pack' ? uploadPack(repo, body, v2) : receivePack(repo, body);
        send(res, 200, reply, `application/x-${git[3]}-result`);
      } catch { throw new HttpError(400, 'Malformed Git request'); }
    } else throw new HttpError(404, 'Not Found');
    return;
  }
  let body: any = {};
  if (method === 'POST' || method === 'PUT' || method === 'PATCH' || method === 'DELETE') {
    const bytes = await readBody(req);
    try { if (bytes.length) body = parseJson(bytes); }
    catch (error) {
      if (path === '/mcp') { json(res, 400, rpcError(null, -32700, 'Parse error')); return; }
      throw error;
    }
  }
  if (path === '/mcp' && method === 'POST') {
    const reply = mcp(store, body, req.headers);
    json(res, reply.status, reply.body); return;
  }
  if (path === '/api/graphql' && method === 'POST') {
    json(res, 200, Array.isArray(body) ? body.map(operation => execute(store, operation)) : execute(store, body)); return;
  }
  if (path === '/api/v3' || path === '/api/v3/') {
    if (method !== 'GET') throw new HttpError(404, 'Not Found');
    json(res, 200, { current_user_url: '/api/v3/user', repository_url: '/api/v3/repos/{owner}/{repo}' }, { 'x-oauth-scopes': 'repo, read:org' }); return;
  }
  if (parts[0] === 'api' && parts[1] === 'v3') {
    body = objectBody(body);
    if (parts.length === 3 && parts[2] === 'meta' && method === 'GET') { json(res, 200, { installed_version: 'forge-1.0.0' }); return; }
    if (parts.length === 3 && parts[2] === 'user' && method === 'GET') { json(res, 200, store.user()); return; }
    if (parts.length === 4 && parts[2] === 'user' && parts[3] === 'repos') {
      if (method === 'POST') { json(res, 201, store.repoView(store.createRepo(body))); return; }
      if (method === 'GET') { json(res, 200, page([...store.repos.values()].map(r => store.repoView(r)), url, res)); return; }
    }
    if (parts[2] === 'repos' && parts.length >= 5) {
      const repo = store.requireRepo(parts[3], parts[4]);
      const tail = parts.slice(5);
      if (repoRoutes(store, repo, method, tail, url, body, res) || issueRoutes(store, repo, method, tail, url, body, res) || pullRoutes(store, repo, method, tail, url, body, res)) return;
    }
  }
  throw new HttpError(404, 'Not Found');
}

export function createApp(): Server {
  const store = new Store();
  const routes = new Map<string, Route>();
  const server = createServer({ noDelay: true, keepAlive: true }, (req, res) => {
    res.sendDate = false;
    void dispatch(store, routes, req, res).catch((error: unknown) => {
      if (error instanceof HttpError) json(res, error.status, { message: error.message });
      else json(res, 400, { message: 'Invalid request' });
    });
  });
  server.keepAliveTimeout = 65000;
  server.headersTimeout = 70000;
  return server;
}
