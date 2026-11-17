import { parse, evaluate, type Field } from './parser.ts';
import { FIELDS, introspect } from './schema.ts';
import type { Store, Repo, Issue, Pull, NodeRecord } from '../store.ts';

type GNode = { __gql: true; type: string; fields: Record<string, any> };
const node = (type: string, fields: Record<string, any>): GNode => ({ __gql: true, type, fields });
const cursor = (index: number) => Buffer.from(`cursor:${index}`).toString('base64');

function connection(items: any[], args: any): any {
  const offset = args.after ? Number(Buffer.from(args.after, 'base64').toString().split(':')[1]) + 1 : 0;
  const start = Number.isSafeInteger(offset) && offset >= 0 ? offset : 0;
  const first = Math.max(0, Math.min(100, args.first ?? 30));
  const selected = items.slice(start, start + first);
  return node('Connection', { nodes: selected, edges: selected.map((item, i) => ({ node: item, cursor: cursor(start + i) })), totalCount: items.length,
    pageInfo: node('PageInfo', { hasNextPage: start + selected.length < items.length, hasPreviousPage: start > 0,
      endCursor: selected.length ? cursor(start + selected.length - 1) : null, startCursor: selected.length ? cursor(start) : null }) });
}

class Executor {
  store: Store;
  constructor(store: Store) { this.store = store; }

  wrap(record?: NodeRecord): GNode | null {
    if (!record) return null;
    const { kind, value, repo } = record;
    if (kind === 'Repository') return this.repository(value);
    if (kind === 'Issue') return this.issue(repo!, value);
    if (kind === 'PullRequest') return this.pull(repo!, value);
    if (kind === 'IssueComment') return this.comment(value);
    return null;
  }

  user(): GNode {
    const user = this.store.user();
    return node('User', { ...user, id: user.node_id, databaseId: user.id,
      repositories: (args: any) => connection([...this.store.repos.values()].map(r => this.repository(r)), args) });
  }

  repository(repo: Repo): GNode {
    const s = this.store;
    return node('Repository', { id: repo.node_id, databaseId: repo.id, name: repo.name, nameWithOwner: `${s.user().login}/${repo.name}`,
      description: repo.description, isPrivate: repo.private, owner: () => this.user(), viewerPermission: 'ADMIN', hasIssuesEnabled: true,
      defaultBranchRef: () => s.resolveRef(repo) ? node('Ref', { id: `${repo.node_id}:${repo.default_branch}`, name: repo.default_branch, target: { oid: s.resolveRef(repo) } }) : null,
      issue: (args: any) => repo.issues.has(args.number) ? this.issue(repo, repo.issues.get(args.number)!) : null,
      issues: (args: any) => {
        const states = args.states as string[] | undefined;
        const direction = args.orderBy?.direction ?? 'DESC';
        const issues = [...repo.issues.values()].filter(i => !states || states.includes(i.state.toUpperCase()))
          .sort((a, b) => direction === 'ASC' ? a.number - b.number : b.number - a.number);
        return connection(issues.map(i => this.issue(repo, i)), args);
      },
      pullRequests: (args: any) => connection([...repo.pulls.values()].filter(p => !args.states || args.states.includes(p.merged ? 'MERGED' : p.state.toUpperCase())).sort((a, b) => b.number - a.number).map(p => this.pull(repo, p)), args),
      pullRequest: (args: any) => repo.pulls.has(args.number) ? this.pull(repo, repo.pulls.get(args.number)!) : null,
    });
  }

  issue(repo: Repo, issue: Issue): GNode {
    return node('Issue', { id: issue.node_id, databaseId: issue.id, number: issue.number, title: issue.title, body: issue.body,
      state: issue.state.toUpperCase(), createdAt: issue.created_at, updatedAt: issue.updated_at, closedAt: issue.closed_at,
      author: () => this.user(), repository: () => this.repository(repo),
      comments: (args: any) => connection(issue.comments.map(c => this.comment(c)), args),
      labels: (args: any) => connection(issue.labels.map(l => ({ ...l, id: l.node_id })), args) });
  }

  comment(comment: any): GNode {
    return node('IssueComment', { id: comment.node_id, databaseId: comment.id, body: comment.body,
      createdAt: comment.created_at, updatedAt: comment.updated_at, author: () => this.user() });
  }

  pull(repo: Repo, pull: Pull): GNode {
    return node('PullRequest', { id: pull.node_id, databaseId: pull.id, number: pull.number, title: pull.title, body: pull.body,
      headRefName: pull.head, baseRefName: pull.base, headRefOid: this.store.resolveRef(repo, pull.head), baseRefOid: this.store.resolveRef(repo, pull.base),
      state: pull.merged ? 'MERGED' : pull.state.toUpperCase(), merged: pull.merged, mergedAt: pull.merged_at,
      mergeable: () => this.store.mergePlan(repo, pull).conflict ? 'CONFLICTING' : 'MERGEABLE',
      author: () => this.user(), repository: () => this.repository(repo),
      files: (args: any) => connection(this.store.pullFiles(repo, pull).map(f => node('PullRequestChangedFile', { path: f.filename, additions: f.additions, deletions: f.deletions, changeType: f.status.toUpperCase() })), args),
    });
  }

  root(mutation: boolean): GNode {
    const s = this.store;
    if (!mutation) return node('Query', {
      viewer: () => this.user(),
      repository: (args: any) => { const repo = s.findRepo(args.owner, args.name); return repo ? this.repository(repo) : null; },
      node: (args: any) => this.wrap(s.nodes.get(args.id)),
      __type: (args: any) => introspect(args.name),
      __schema: () => ({ queryType: { name: 'Query' }, mutationType: { name: 'Mutation' }, types: Object.keys(FIELDS).map(introspect), directives: [] }),
    });
    const get = (id: string, kind: string): NodeRecord => {
      const found = s.nodes.get(id);
      if (!found || found.kind !== kind) throw new Error(`${kind} not found`);
      return found;
    };
    return node('Mutation', {
      createIssue: (args: any) => {
        const input = args.input ?? {}, repo = get(input.repositoryId, 'Repository').value;
        const issue = s.createIssue(repo, input);
        return { issue: this.issue(repo, issue), clientMutationId: input.clientMutationId ?? null };
      },
      closeIssue: (args: any) => {
        const input = args.input ?? {}, found = get(input.issueId, 'Issue');
        s.patchIssue(found.repo!, found.value, { state: 'closed' });
        return { issue: this.issue(found.repo!, found.value), clientMutationId: input.clientMutationId ?? null };
      },
      createPullRequest: (args: any) => {
        const input = args.input ?? {}, repo = get(input.repositoryId, 'Repository').value;
        const pull = s.createPull(repo, { title: input.title, body: input.body, head: input.headRefName, base: input.baseRefName });
        return { pullRequest: this.pull(repo, pull), clientMutationId: input.clientMutationId ?? null };
      },
      mergePullRequest: (args: any) => {
        const input = args.input ?? {}, found = get(input.pullRequestId, 'PullRequest');
        s.mergePull(found.repo!, found.value, input);
        return { pullRequest: this.pull(found.repo!, found.value), clientMutationId: input.clientMutationId ?? null };
      },
      addComment: (args: any) => {
        const input = args.input ?? {}, found = get(input.subjectId, 'Issue');
        const comment = s.comment(found.repo!, found.value, input.body);
        return { commentEdge: { node: this.comment(comment), cursor: cursor(found.value.comments.length - 1) }, clientMutationId: input.clientMutationId ?? null };
      },
    });
  }
}

export function execute(store: Store, request: any): any {
  try {
    if (!request || typeof request.query !== 'string') throw new Error('query is required');
    const document = parse(request.query);
    const operation = request.operationName ? document.operations.find(o => o.name === request.operationName) : document.operations[0];
    if (!operation) throw new Error('Operation not found');
    const variables = { ...operation.defaults, ...request.variables };
    const executor = new Executor(store);
    const project = (value: any, fields: Field[], depth = 0): any => {
      if (depth > 64) throw new Error('GraphQL response too deep');
      if (value === null || value === undefined) return null;
      if (Array.isArray(value)) return value.map(item => project(item, fields, depth + 1));
      if (!fields.length) return value;
      const result: Record<string, any> = {};
      const type = value.__gql ? value.type : value.__typename;
      for (const field of fields) {
        if (field.directives.some(d => (d.name === 'skip' && evaluate(d.args.if, variables)) || (d.name === 'include' && !evaluate(d.args.if, variables)))) continue;
        if (field.spread || field.condition) {
          const fragment = field.spread ? document.fragments.get(field.spread) : field;
          if (!fragment) throw new Error(`Unknown fragment ${field.spread}`);
          if (!fragment.condition || !type || fragment.condition === type) Object.assign(result, project(value, fragment.children, depth + 1));
          continue;
        }
        if (field.name === '__typename') { result[field.alias] = type ?? 'Object'; continue; }
        const properties = value.__gql ? value.fields : value;
        if (!Object.hasOwn(properties, field.name)) throw new Error(`Cannot query field "${field.name}" on type "${type ?? 'Object'}".`);
        const resolver = properties[field.name];
        const resolved = typeof resolver === 'function' ? resolver(evaluate(field.args, variables)) : resolver;
        result[field.alias] = project(resolved, field.children, depth + 1);
      }
      return result;
    };
    return { data: project(executor.root(operation.type === 'mutation'), operation.fields) };
  } catch (error) {
    return { data: null, errors: [{ message: error instanceof Error ? error.message : 'Invalid GraphQL request' }] };
  }
}
