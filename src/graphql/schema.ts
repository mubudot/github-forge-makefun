/** The public schema is kept separate from execution so introspection and validation agree. */
export const FIELDS: Record<string, string[]> = {
  Query: ['viewer', 'repository', 'node', '__type', '__schema'],
  Mutation: ['createIssue', 'closeIssue', 'createPullRequest', 'mergePullRequest', 'addComment'],
  User: ['id', 'databaseId', 'login', 'name', 'repositories'],
  Repository: ['id', 'databaseId', 'name', 'nameWithOwner', 'description', 'isPrivate', 'owner', 'defaultBranchRef', 'viewerPermission', 'hasIssuesEnabled', 'issue', 'issues', 'pullRequests', 'pullRequest'],
  Issue: ['id', 'databaseId', 'number', 'title', 'body', 'state', 'createdAt', 'updatedAt', 'closedAt', 'author', 'comments', 'labels', 'repository'],
  PullRequest: ['id', 'databaseId', 'number', 'title', 'body', 'headRefName', 'baseRefName', 'headRefOid', 'baseRefOid', 'mergeable', 'state', 'merged', 'mergedAt', 'files', 'repository', 'author'],
  IssueComment: ['id', 'databaseId', 'body', 'createdAt', 'updatedAt', 'author'],
  Ref: ['id', 'name', 'target'],
  PullRequestChangedFile: ['path', 'additions', 'deletions', 'changeType'],
  Label: ['id', 'name', 'color', 'description'],
  PageInfo: ['hasNextPage', 'hasPreviousPage', 'endCursor', 'startCursor'],
};
export const ENUMS: Record<string, string[]> = {
  SearchType: ['ISSUE', 'REPOSITORY', 'USER'], IssueState: ['OPEN', 'CLOSED'], PullRequestState: ['OPEN', 'CLOSED', 'MERGED'],
  MergeableState: ['MERGEABLE', 'CONFLICTING', 'UNKNOWN'], RepositoryPermission: ['ADMIN', 'MAINTAIN', 'WRITE', 'TRIAGE', 'READ'],
};

export function introspect(name: string): any {
  if (!FIELDS[name] && !ENUMS[name]) return null;
  return {
    name, kind: ENUMS[name] ? 'ENUM' : 'OBJECT', description: `${name} in the forge API`,
    fields: FIELDS[name]?.map(name => ({ name, description: null, args: [], isDeprecated: false, deprecationReason: null,
      type: { kind: 'SCALAR', name: name === 'number' || name === 'databaseId' ? 'Int' : 'String', ofType: null } })) ?? null,
    enumValues: ENUMS[name]?.map(name => ({ name, description: null, isDeprecated: false, deprecationReason: null })) ?? null,
    interfaces: [], possibleTypes: null,
  };
}
