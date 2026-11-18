package graphql

var fields = map[string][]string{
	"Query":        {"viewer", "repository", "node", "__type", "__schema"},
	"Mutation":     {"createIssue", "closeIssue", "createPullRequest", "mergePullRequest", "addComment"},
	"User":         {"id", "databaseId", "login", "name", "repositories"},
	"Repository":   {"id", "databaseId", "name", "nameWithOwner", "description", "isPrivate", "owner", "defaultBranchRef", "viewerPermission", "hasIssuesEnabled", "issue", "issues", "pullRequests", "pullRequest"},
	"Issue":        {"id", "databaseId", "number", "title", "body", "state", "createdAt", "updatedAt", "closedAt", "author", "comments", "labels", "repository"},
	"PullRequest":  {"id", "databaseId", "number", "title", "body", "headRefName", "baseRefName", "headRefOid", "baseRefOid", "mergeable", "state", "merged", "mergedAt", "files", "repository", "author"},
	"IssueComment": {"id", "databaseId", "body", "createdAt", "updatedAt", "author"},
	"Ref":          {"id", "name", "target"}, "PullRequestChangedFile": {"path", "additions", "deletions", "changeType"},
	"Label": {"id", "name", "color", "description"}, "PageInfo": {"hasNextPage", "hasPreviousPage", "endCursor", "startCursor"},
}
var enums = map[string][]string{"SearchType": {"ISSUE", "REPOSITORY", "USER"}, "IssueState": {"OPEN", "CLOSED"}, "PullRequestState": {"OPEN", "CLOSED", "MERGED"}, "MergeableState": {"MERGEABLE", "CONFLICTING", "UNKNOWN"}, "RepositoryPermission": {"ADMIN", "MAINTAIN", "WRITE", "TRIAGE", "READ"}}

func introspect(name string) any {
	f, ok := fields[name]
	e, enum := enums[name]
	if !ok && !enum {
		return nil
	}
	kind := "OBJECT"
	if enum {
		kind = "ENUM"
	}
	var fieldValues, enumValues any
	if ok {
		list := []any{}
		for _, name := range f {
			scalar := "String"
			if name == "number" || name == "databaseId" {
				scalar = "Int"
			}
			list = append(list, M{"name": name, "description": nil, "args": []any{}, "isDeprecated": false, "deprecationReason": nil, "type": M{"kind": "SCALAR", "name": scalar, "ofType": nil}})
		}
		fieldValues = list
	}
	if enum {
		list := []any{}
		for _, name := range e {
			list = append(list, M{"name": name, "description": nil, "isDeprecated": false, "deprecationReason": nil})
		}
		enumValues = list
	}
	return M{"name": name, "kind": kind, "description": name + " in the forge API", "fields": fieldValues, "enumValues": enumValues, "interfaces": []any{}, "possibleTypes": nil}
}
