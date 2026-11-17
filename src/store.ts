import { HttpError, requiredString } from './http.ts';
import { ObjectDatabase, type TreeEntry } from './git/objects.ts';

export type Label = { id: number; node_id: string; name: string; color: string; description: string };
export type Comment = { id: number; node_id: string; body: string; created_at: string; updated_at: string };
export type Issue = {
  id: number; node_id: string; number: number; title: string; body: string; state: string;
  labels: Label[]; created_at: string; updated_at: string; closed_at: string | null;
  comments: Comment[]; serialized?: Buffer;
};
export type Pull = {
  id: number; node_id: string; number: number; title: string; body: string; state: string;
  head: string; base: string; created_at: string; updated_at: string;
  merged: boolean; merged_at: string | null; merge_commit_sha: string | null;
};
export type Repo = {
  id: number; node_id: string; name: string; description: string; private: boolean; default_branch: string;
  created_at: string; db: ObjectDatabase; refs: Map<string, string>;
  labels: Map<string, Label>; issues: Map<number, Issue>; pulls: Map<number, Pull>; nextNumber: number;
  fetchCache: Map<string, Buffer>;
};
export type NodeRecord = { kind: string; value: any; repo?: Repo };
export const globalId = (type: string, id: number): string => Buffer.from(`${type}:${id}`).toString('base64');

export class Store {
  repos = new Map<string, Repo>();
  nodes = new Map<string, NodeRecord>();
  login = process.env.FORGE_OWNER_LOGIN ?? '';
  private cachedUser: any;
  nextId = 1;
  epoch: number;
  now: string;

  constructor() {
    const date = Date.parse(process.env.MAKEFUN_TRIAL_NOW ?? '2026-01-01T00:00:00Z');
    this.epoch = Math.floor((Number.isFinite(date) ? date : 1767225600000) / 1000);
    this.now = new Date(this.epoch * 1000).toISOString();
  }

  user(): any {
    const login = this.login || 'mubudot';
    if (this.cachedUser?.login !== login) this.cachedUser = { login, id: 1, node_id: globalId('User', 1), name: 'Ethan Moore', type: 'User' };
    return this.cachedUser;
  }

  identity(kind: string, value: any, repo?: Repo): void {
    value.id = this.nextId++;
    value.node_id = globalId(kind, value.id);
    this.nodes.set(value.node_id, { kind, value, repo });
  }

  createRepo(input: any): Repo {
    const name = requiredString(input.name, 'name');
    if (!/^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$/.test(name)) throw new HttpError(422, 'Invalid repository name');
    if (this.repos.has(name)) throw new HttpError(422, 'Repository already exists');
    const repo = {
      name, description: input.description ?? '', private: input.private === true, default_branch: 'main',
      created_at: this.now, db: new ObjectDatabase(), refs: new Map(), labels: new Map(), issues: new Map(), pulls: new Map(), nextNumber: 1,
      fetchCache: new Map(),
    } as Repo;
    this.identity('Repository', repo);
    this.repos.set(name, repo);
    if (input.auto_init === true) {
      const blob = repo.db.put('blob', Buffer.from(`# ${name}\n`));
      const tree = repo.db.writeTree([{ mode: '100644', name: 'README.md', oid: blob }]);
      repo.refs.set('refs/heads/main', repo.db.writeCommit(tree, [], 'Initial commit', this.epoch));
    }
    return repo;
  }

  /** A single-owner instance may provision its namespace at its first authenticated repository address. */
  findRepo(owner: string, name: string): Repo | undefined {
    const repo = this.repos.get(name);
    if (!repo) return;
    if (!this.login) this.login = owner;
    return this.login === owner ? repo : undefined;
  }

  requireRepo(owner: string, name: string): Repo {
    const repo = this.findRepo(owner, name);
    if (!repo) throw new HttpError(404, 'Not Found');
    return repo;
  }

  repoView(repo: Repo): any {
    const owner = this.user();
    return { id: repo.id, node_id: repo.node_id, name: repo.name, full_name: `${owner.login}/${repo.name}`, owner,
      private: repo.private, description: repo.description, default_branch: repo.default_branch, has_issues: true,
      created_at: repo.created_at, updated_at: repo.created_at, permissions: { admin: true, push: true, pull: true } };
  }

  deleteRepo(repo: Repo): void {
    this.repos.delete(repo.name);
    for (const [key, node] of this.nodes) if (node.value === repo || node.repo === repo) this.nodes.delete(key);
  }

  createLabel(repo: Repo, input: any): Label {
    const name = requiredString(input.name, 'name');
    if (repo.labels.has(name)) throw new HttpError(422, 'Label already exists');
    const label = { name, color: input.color ?? 'ededed', description: input.description ?? '' } as Label;
    this.identity('Label', label, repo);
    repo.labels.set(name, label);
    return label;
  }

  issueLabels(repo: Repo, names: any = []): Label[] {
    if (!Array.isArray(names)) throw new HttpError(422, 'labels must be an array');
    return names.map(name => {
      const label = repo.labels.get(typeof name === 'string' ? name : name?.name);
      if (!label) throw new HttpError(422, 'Label does not exist');
      return label;
    });
  }

  createIssue(repo: Repo, input: any): Issue {
    const title = requiredString(input.title, 'title');
    const labels = this.issueLabels(repo, input.labels);
    const issue = { number: repo.nextNumber++, title, body: input.body ?? '', state: 'open', labels,
      created_at: this.now, updated_at: this.now, closed_at: null, comments: [] } as Issue;
    this.identity('Issue', issue, repo);
    repo.issues.set(issue.number, issue);
    return issue;
  }

  issueView(issue: Issue): any {
    return { id: issue.id, node_id: issue.node_id, number: issue.number, title: issue.title, body: issue.body, state: issue.state,
      labels: issue.labels, user: this.user(), created_at: issue.created_at, updated_at: issue.updated_at, closed_at: issue.closed_at,
      comments: issue.comments.length };
  }

  patchIssue(repo: Repo, issue: Issue, input: any): Issue {
    if (input.title !== undefined) issue.title = requiredString(input.title, 'title');
    if (input.body !== undefined) issue.body = input.body ?? '';
    if (input.labels !== undefined) issue.labels = this.issueLabels(repo, input.labels);
    if (input.state !== undefined) {
      if (!['open', 'closed'].includes(input.state)) throw new HttpError(422, 'Invalid issue state');
      issue.state = input.state;
      issue.closed_at = input.state === 'closed' ? this.now : null;
    }
    issue.updated_at = this.now;
    issue.serialized = undefined;
    return issue;
  }

  listIssues(repo: Repo, input: any = {}): Issue[] {
    const state = (input.state ?? 'open').toLowerCase();
    return [...repo.issues.values()].filter(i => state === 'all' || i.state === state)
      .sort((a, b) => input.direction === 'asc' ? a.number - b.number : b.number - a.number);
  }

  comment(repo: Repo, issue: Issue, body: string): Comment {
    requiredString(body, 'body');
    const comment = { body, created_at: this.now, updated_at: this.now } as Comment;
    this.identity('IssueComment', comment, repo);
    issue.comments.push(comment);
    issue.serialized = undefined;
    return comment;
  }

  resolveRef(repo: Repo, ref?: string): string {
    const key = ref || repo.default_branch;
    return repo.refs.get(key) ?? repo.refs.get(`refs/heads/${key}`) ?? repo.refs.get(`refs/tags/${key}`) ?? (repo.db.objects.has(key) ? key : '');
  }

  updateRef(repo: Repo, ref: string, oid: string): void {
    repo.refs.set(ref, oid);
    repo.fetchCache.clear();
  }

  createPull(repo: Repo, input: any): Pull {
    const title = requiredString(input.title, 'title');
    const head = requiredString(input.head, 'head').split(':').at(-1)!;
    const base = requiredString(input.base, 'base');
    if (!this.resolveRef(repo, head) || !this.resolveRef(repo, base)) throw new HttpError(422, 'Head or base branch does not exist');
    if (head === base) throw new HttpError(422, 'Head and base must differ');
    if ([...repo.pulls.values()].some(p => p.head === head && p.base === base && p.state === 'open')) throw new HttpError(422, 'Pull request already exists');
    const pull = { number: repo.nextNumber++, title, body: input.body ?? '', state: 'open', head, base, created_at: this.now,
      updated_at: this.now, merged: false, merged_at: null, merge_commit_sha: null } as Pull;
    this.identity('PullRequest', pull, repo);
    repo.pulls.set(pull.number, pull);
    return pull;
  }

  pullView(repo: Repo, pull: Pull): any {
    return { id: pull.id, node_id: pull.node_id, number: pull.number, title: pull.title, body: pull.body, state: pull.state,
      head: { ref: pull.head, sha: this.resolveRef(repo, pull.head) }, base: { ref: pull.base, sha: this.resolveRef(repo, pull.base) },
      user: this.user(), created_at: pull.created_at, updated_at: pull.updated_at, merged: pull.merged,
      merged_at: pull.merged_at, merge_commit_sha: pull.merge_commit_sha, mergeable: !this.mergePlan(repo, pull).conflict };
  }

  pullFiles(repo: Repo, pull: Pull): any[] {
    const head = this.resolveRef(repo, pull.head), base = this.resolveRef(repo, pull.base);
    const ancestor = repo.db.mergeBase(base, head);
    const oldFiles = repo.db.files(ancestor), newFiles = repo.db.files(head);
    const paths = [...new Set([...oldFiles.keys(), ...newFiles.keys()])].sort();
    const lineCount = (entry?: TreeEntry) => entry ? repo.db.get(entry.oid).data.toString().split('\n').length - (repo.db.get(entry.oid).data.at(-1) === 10 ? 1 : 0) : 0;
    return paths.filter(path => oldFiles.get(path)?.oid !== newFiles.get(path)?.oid).map(filename => {
      const old = oldFiles.get(filename), next = newFiles.get(filename);
      const additions = Math.max(0, lineCount(next)), deletions = Math.max(0, lineCount(old));
      return { filename, path: filename, status: !old ? 'added' : !next ? 'removed' : 'modified', additions, deletions,
        changes: additions + deletions, sha: next?.oid ?? old?.oid };
    });
  }

  mergePlan(repo: Repo, pull: Pull): { files: Map<string, TreeEntry>; conflict: boolean } {
    const head = this.resolveRef(repo, pull.head), base = this.resolveRef(repo, pull.base);
    const ancestor = repo.db.mergeBase(base, head);
    const original = repo.db.files(ancestor), current = repo.db.files(base), incoming = repo.db.files(head);
    let conflict = false;
    for (const path of new Set([...original.keys(), ...incoming.keys()])) {
      const before = original.get(path)?.oid, after = incoming.get(path)?.oid;
      if (before === after) continue;
      const existing = current.get(path)?.oid;
      if (existing !== before && existing !== after) conflict = true;
      if (after) current.set(path, incoming.get(path)!); else current.delete(path);
    }
    return { files: current, conflict };
  }

  mergePull(repo: Repo, pull: Pull, input: any = {}): any {
    if (pull.merged) return { sha: pull.merge_commit_sha, merged: true, message: 'Pull Request successfully merged' };
    if (pull.state !== 'open') throw new HttpError(405, 'Pull request is closed');
    const plan = this.mergePlan(repo, pull);
    if (plan.conflict) throw new HttpError(405, 'Pull Request is not mergeable');
    const tree = repo.db.treeFromFiles(plan.files);
    const base = this.resolveRef(repo, pull.base);
    const sha = repo.db.writeCommit(tree, [base], input.commit_title ?? `${pull.title} (#${pull.number})`, this.epoch);
    this.updateRef(repo, `refs/heads/${pull.base}`, sha);
    pull.state = 'closed'; pull.merged = true; pull.merged_at = this.now; pull.merge_commit_sha = sha;
    return { sha, merged: true, message: 'Pull Request successfully merged' };
  }
}
