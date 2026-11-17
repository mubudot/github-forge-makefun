import type { Store } from './store.ts';

export const REVISIONS = ['2026-07-28', '2025-11-25', '2025-06-18', '2025-03-26', '2024-11-05'];
const SERVER = { name: 'forge', version: '1.0.0' };
const CAPABILITIES = { tools: { listChanged: false } };
const str = { type: 'string' }, num = { type: 'integer' };
const repoProperties = { owner: str, repo: str };
const tool = (name: string, description: string, properties: Record<string, any>, required: string[]) => ({
  name, description, inputSchema: { type: 'object', properties, required, additionalProperties: false },
});
export const TOOLS = [
  tool('create_repo', 'Create a repository owned by the authenticated user.', { name: str, description: str, private: { type: 'boolean' } }, ['name']),
  tool('get_repo', 'Get repository details by owner and repository name.', repoProperties, ['owner', 'repo']),
  tool('create_issue', 'Open an issue in a repository.', { ...repoProperties, title: str, body: str }, ['owner', 'repo', 'title']),
  tool('list_issues', 'List repository issues filtered by open, closed, or all states.', { ...repoProperties, state: { type: 'string', enum: ['open', 'closed', 'all'] } }, ['owner', 'repo']),
  tool('create_pull_request', 'Open a pull request from a head branch into a base branch.', { ...repoProperties, title: str, body: str, head: str, base: str }, ['owner', 'repo', 'title', 'head', 'base']),
  tool('merge_pull_request', 'Squash merge a pull request into its base branch.', { ...repoProperties, pull_number: num }, ['owner', 'repo', 'pull_number']),
];

export function rpcError(id: any, code: number, message: string, data?: any): any {
  return { jsonrpc: '2.0', id: id ?? null, error: { code, message, ...(data === undefined ? {} : { data }) } };
}

export function mcp(store: Store, request: any, headers: Record<string, any>): { status: number; body: any } {
  if (!request || typeof request !== 'object' || Array.isArray(request) || request.jsonrpc !== '2.0' || typeof request.method !== 'string') {
    return { status: 400, body: rpcError(request?.id, -32600, 'Invalid Request') };
  }
  const { id, method } = request, params = request.params ?? {};
  const error = (code: number, message: string, data?: any) => ({ status: 200, body: rpcError(id, code, message, data) });
  const success = (result: any) => ({ status: 200, body: { jsonrpc: '2.0', id, result } });
  const revision = headers['mcp-protocol-version'] ?? params._meta?.['io.modelcontextprotocol/protocolVersion'];
  if (revision && !REVISIONS.includes(revision)) return error(-32022, 'Unsupported protocol version', { supported: REVISIONS });
  if (headers['mcp-method'] && headers['mcp-method'] !== method) return error(-32020, 'Mcp-Method does not match request method');
  if (method.startsWith('notifications/')) return { status: 202, body: null };
  if (id === undefined) return { status: 202, body: null };
  if (method === 'server/discover') return success({ resultType: 'server/discover', supportedVersions: REVISIONS,
    capabilities: CAPABILITIES, serverInfo: SERVER, _meta: { 'io.modelcontextprotocol/serverInfo': SERVER } });
  if (method === 'initialize') return success({ protocolVersion: REVISIONS.includes(params.protocolVersion) ? params.protocolVersion : REVISIONS[0], capabilities: CAPABILITIES, serverInfo: SERVER });
  if (method === 'ping') return success({});
  if (method === 'tools/list') return success({ tools: TOOLS });
  if (method !== 'tools/call') return error(-32601, 'Method not found');
  const selected = TOOLS.find(t => t.name === params.name);
  if (!selected) return error(-32602, 'Unknown tool');
  const args = params.arguments;
  if (!args || typeof args !== 'object' || Array.isArray(args)) return error(-32602, 'arguments must be an object');
  for (const key of selected.inputSchema.required) {
    if (args[key] === undefined || args[key] === null || args[key] === '') return error(-32602, `Missing required argument: ${key}`);
  }
  for (const [key, value] of Object.entries(args)) {
    const property = selected.inputSchema.properties[key];
    if (!property) return error(-32602, `Unknown argument: ${key}`);
    if (property.type === 'integer' ? !Number.isSafeInteger(value) : typeof value !== property.type) return error(-32602, `Invalid type for ${key}`);
    if (property.enum && !property.enum.includes(value)) return error(-32602, `Invalid value for ${key}`);
  }
  try {
    let result: any;
    if (selected.name === 'create_repo') result = store.repoView(store.createRepo(args));
    else {
      const repo = store.requireRepo(args.owner, args.repo);
      if (selected.name === 'get_repo') result = store.repoView(repo);
      if (selected.name === 'create_issue') result = store.issueView(store.createIssue(repo, args));
      if (selected.name === 'list_issues') result = { issues: store.listIssues(repo, args).map(i => store.issueView(i)) };
      if (selected.name === 'create_pull_request') result = store.pullView(repo, store.createPull(repo, args));
      if (selected.name === 'merge_pull_request') {
        const pull = repo.pulls.get(args.pull_number);
        if (!pull) throw new Error('Pull request not found');
        result = store.mergePull(repo, pull);
      }
    }
    return success({ content: [{ type: 'text', text: JSON.stringify(result) }], structuredContent: result, isError: false });
  } catch (error) {
    return success({ content: [{ type: 'text', text: error instanceof Error ? error.message : 'Tool failed' }], isError: true });
  }
}
