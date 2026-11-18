package graphql

import (
	"encoding/base64"
	"forge/store"
	"sort"
	"strconv"
	"strings"
)

type node struct {
	kind   string
	values M
}
type resolver func(M) any

func n(kind string, values M) *node { return &node{kind, values} }
func cursor(i int) string {
	return base64.StdEncoding.EncodeToString([]byte("cursor:" + strconv.Itoa(i)))
}
func connection(items []any, args M) any {
	start := 0
	if after := store.Str(args["after"]); after != "" {
		b, e := base64.StdEncoding.DecodeString(after)
		if e == nil {
			_, s, ok := strings.Cut(string(b), ":")
			if v, e := strconv.Atoi(s); ok && e == nil && v >= -1 {
				start = v + 1
			}
		}
	}
	first := 30
	if v, ok := args["first"]; ok {
		first = store.Num(v)
	}
	first = max(0, min(100, first))
	end := min(len(items), start+first)
	start = min(start, len(items))
	selected := items[start:end]
	edges := []any{}
	for i, item := range selected {
		edges = append(edges, M{"node": item, "cursor": cursor(start + i)})
	}
	var endCursor, startCursor any
	if len(selected) > 0 {
		endCursor = cursor(end - 1)
		startCursor = cursor(start)
	}
	return n("Connection", M{"nodes": selected, "edges": edges, "totalCount": len(items), "pageInfo": n("PageInfo", M{"hasNextPage": end < len(items), "hasPreviousPage": start > 0, "endCursor": endCursor, "startCursor": startCursor})})
}

type executor struct{ s *store.Store }

func (e executor) wrap(record store.NodeRecord) any {
	switch record.Kind {
	case "Repository":
		return e.repository(record.Value.(*store.Repo))
	case "Issue":
		return e.issue(record.Repo, record.Value.(*store.Issue))
	case "PullRequest":
		return e.pull(record.Repo, record.Value.(*store.Pull))
	case "IssueComment":
		return e.comment(record.Value.(*store.Comment))
	}
	return nil
}
func (e executor) user() *node {
	u := e.s.User()
	return n("User", M{"id": u["node_id"], "databaseId": u["id"], "login": u["login"], "name": u["name"], "type": u["type"], "node_id": u["node_id"], "repositories": resolver(func(a M) any {
		out := []any{}
		for _, r := range e.s.RepoOrder {
			out = append(out, e.repository(r))
		}
		return connection(out, a)
	})})
}
func hasState(states any, state string) bool {
	if states == nil {
		return true
	}
	for _, v := range states.([]any) {
		if v == state {
			return true
		}
	}
	return false
}
func (e executor) repository(r *store.Repo) *node {
	s := e.s
	return n("Repository", M{"id": r.NodeID, "databaseId": r.ID, "name": r.Name, "nameWithOwner": store.Str(s.User()["login"]) + "/" + r.Name, "description": r.Description, "isPrivate": r.Private, "owner": resolver(func(M) any { return e.user() }), "viewerPermission": "ADMIN", "hasIssuesEnabled": true,
		"defaultBranchRef": resolver(func(M) any {
			tip := s.ResolveRef(r, "")
			if tip == "" {
				return nil
			}
			return n("Ref", M{"id": r.NodeID + ":" + r.DefaultBranch, "name": r.DefaultBranch, "target": M{"oid": tip}})
		}),
		"issue": resolver(func(a M) any {
			i := r.Issues[store.Num(a["number"])]
			if i == nil {
				return nil
			}
			return e.issue(r, i)
		}),
		"issues": resolver(func(a M) any {
			issues := []*store.Issue{}
			for _, i := range r.Issues {
				if hasState(a["states"], strings.ToUpper(i.State)) {
					issues = append(issues, i)
				}
			}
			asc := store.Obj(a["orderBy"])["direction"] == "ASC"
			sort.Slice(issues, func(i, j int) bool {
				if asc {
					return issues[i].Number < issues[j].Number
				}
				return issues[i].Number > issues[j].Number
			})
			out := []any{}
			for _, i := range issues {
				out = append(out, e.issue(r, i))
			}
			return connection(out, a)
		}),
		"pullRequests": resolver(func(a M) any {
			pulls := []*store.Pull{}
			for _, p := range r.Pulls {
				state := strings.ToUpper(p.State)
				if p.Merged {
					state = "MERGED"
				}
				if hasState(a["states"], state) {
					pulls = append(pulls, p)
				}
			}
			sort.Slice(pulls, func(i, j int) bool { return pulls[i].Number > pulls[j].Number })
			out := []any{}
			for _, p := range pulls {
				out = append(out, e.pull(r, p))
			}
			return connection(out, a)
		}),
		"pullRequest": resolver(func(a M) any {
			p := r.Pulls[store.Num(a["number"])]
			if p == nil {
				return nil
			}
			return e.pull(r, p)
		}),
	})
}
func (e executor) issue(r *store.Repo, i *store.Issue) *node {
	return n("Issue", M{"id": i.NodeID, "databaseId": i.ID, "number": i.Number, "title": i.Title, "body": i.Body, "state": strings.ToUpper(i.State), "createdAt": i.CreatedAt, "updatedAt": i.UpdatedAt, "closedAt": i.ClosedAt, "author": resolver(func(M) any { return e.user() }), "repository": resolver(func(M) any { return e.repository(r) }), "comments": resolver(func(a M) any {
		out := []any{}
		for _, c := range i.Comments {
			out = append(out, e.comment(c))
		}
		return connection(out, a)
	}), "labels": resolver(func(a M) any {
		out := []any{}
		for _, l := range i.Labels {
			out = append(out, M{"id": l.NodeID, "node_id": l.NodeID, "name": l.Name, "color": l.Color, "description": l.Description})
		}
		return connection(out, a)
	})})
}
func (e executor) comment(c *store.Comment) *node {
	return n("IssueComment", M{"id": c.NodeID, "databaseId": c.ID, "body": c.Body, "createdAt": c.CreatedAt, "updatedAt": c.UpdatedAt, "author": resolver(func(M) any { return e.user() })})
}
func (e executor) pull(r *store.Repo, p *store.Pull) *node {
	state := strings.ToUpper(p.State)
	if p.Merged {
		state = "MERGED"
	}
	return n("PullRequest", M{"id": p.NodeID, "databaseId": p.ID, "number": p.Number, "title": p.Title, "body": p.Body, "headRefName": p.Head, "baseRefName": p.Base, "headRefOid": e.s.ResolveRef(r, p.Head), "baseRefOid": e.s.ResolveRef(r, p.Base), "state": state, "merged": p.Merged, "mergedAt": p.MergedAt, "mergeable": resolver(func(M) any {
		_, conflict := e.s.MergePlan(r, p)
		if conflict {
			return "CONFLICTING"
		}
		return "MERGEABLE"
	}), "author": resolver(func(M) any { return e.user() }), "repository": resolver(func(M) any { return e.repository(r) }), "files": resolver(func(a M) any {
		out := []any{}
		for _, v := range e.s.PullFiles(r, p) {
			f := store.Obj(v)
			out = append(out, n("PullRequestChangedFile", M{"path": f["filename"], "additions": f["additions"], "deletions": f["deletions"], "changeType": strings.ToUpper(store.Str(f["status"]))}))
		}
		return connection(out, a)
	})})
}
func (e executor) get(id, kind string) store.NodeRecord {
	record, ok := e.s.Nodes[id]
	if !ok || record.Kind != kind {
		panic(kind + " not found")
	}
	return record
}
func (e executor) root(mutation bool) *node {
	s := e.s
	if !mutation {
		return n("Query", M{"viewer": resolver(func(M) any { return e.user() }), "repository": resolver(func(a M) any {
			r := s.FindRepo(store.Str(a["owner"]), store.Str(a["name"]))
			if r == nil {
				return nil
			}
			return e.repository(r)
		}), "node": resolver(func(a M) any { return e.wrap(s.Nodes[store.Str(a["id"])]) }), "__type": resolver(func(a M) any { return introspect(store.Str(a["name"])) }), "__schema": resolver(func(M) any {
			names := []string{}
			for name := range fields {
				names = append(names, name)
			}
			sort.Strings(names)
			types := []any{}
			for _, name := range names {
				types = append(types, introspect(name))
			}
			return M{"queryType": M{"name": "Query"}, "mutationType": M{"name": "Mutation"}, "types": types, "directives": []any{}}
		})})
	}
	return n("Mutation", M{
		"createIssue": resolver(func(a M) any {
			in := store.Obj(a["input"])
			r := e.get(store.Str(in["repositoryId"]), "Repository").Value.(*store.Repo)
			i := s.CreateIssue(r, in)
			return M{"issue": e.issue(r, i), "clientMutationId": in["clientMutationId"]}
		}),
		"closeIssue": resolver(func(a M) any {
			in := store.Obj(a["input"])
			rec := e.get(store.Str(in["issueId"]), "Issue")
			i := rec.Value.(*store.Issue)
			s.PatchIssue(rec.Repo, i, M{"state": "closed"})
			return M{"issue": e.issue(rec.Repo, i), "clientMutationId": in["clientMutationId"]}
		}),
		"createPullRequest": resolver(func(a M) any {
			in := store.Obj(a["input"])
			r := e.get(store.Str(in["repositoryId"]), "Repository").Value.(*store.Repo)
			p := s.CreatePull(r, M{"title": in["title"], "body": in["body"], "head": in["headRefName"], "base": in["baseRefName"]})
			return M{"pullRequest": e.pull(r, p), "clientMutationId": in["clientMutationId"]}
		}),
		"mergePullRequest": resolver(func(a M) any {
			in := store.Obj(a["input"])
			rec := e.get(store.Str(in["pullRequestId"]), "PullRequest")
			p := rec.Value.(*store.Pull)
			s.MergePull(rec.Repo, p, in)
			return M{"pullRequest": e.pull(rec.Repo, p), "clientMutationId": in["clientMutationId"]}
		}),
		"addComment": resolver(func(a M) any {
			in := store.Obj(a["input"])
			rec := e.get(store.Str(in["subjectId"]), "Issue")
			i := rec.Value.(*store.Issue)
			c := s.Comment(rec.Repo, i, store.Str(in["body"]))
			return M{"commentEdge": M{"node": e.comment(c), "cursor": cursor(len(i.Comments) - 1)}, "clientMutationId": in["clientMutationId"]}
		}),
	})
}
func truth(v any) bool {
	if v == nil || v == false || v == "" || v == float64(0) || v == 0 {
		return false
	}
	return true
}
func Execute(s *store.Store, request any) (result any) {
	defer func() {
		if r := recover(); r != nil {
			result = M{"data": nil, "errors": []any{M{"message": store.Message(r)}}}
		}
	}()
	req := store.Obj(request)
	source, ok := req["query"].(string)
	if !ok {
		panic("query is required")
	}
	doc := parse(source)
	op := doc.operations[0]
	if name := store.Str(req["operationName"]); name != "" {
		found := false
		for _, o := range doc.operations {
			if o.name == name {
				op = o
				found = true
				break
			}
		}
		if !found {
			panic("Operation not found")
		}
	}
	vars := M{}
	for k, v := range op.defaults {
		vars[k] = v
	}
	for k, v := range store.Obj(req["variables"]) {
		vars[k] = v
	}
	var project func(any, []field, int) any
	project = func(value any, selection []field, depth int) any {
		if depth > 64 {
			panic("GraphQL response too deep")
		}
		if value == nil {
			return nil
		}
		if list, ok := value.([]any); ok {
			out := make([]any, len(list))
			for i, item := range list {
				out[i] = project(item, selection, depth+1)
			}
			return out
		}
		if len(selection) == 0 {
			return value
		}
		kind := ""
		properties := M{}
		switch v := value.(type) {
		case *node:
			kind = v.kind
			properties = v.values
		case map[string]any:
			properties = v
			kind = store.Str(v["__typename"])
		default:
			panic("Invalid selection")
		}
		out := M{}
		for _, f := range selection {
			skip := false
			for _, d := range f.directives {
				v := truth(evaluate(d.args["if"], vars))
				if d.name == "skip" && v || d.name == "include" && !v {
					skip = true
				}
			}
			if skip {
				continue
			}
			if f.spread != "" || f.condition != "" {
				fragment := f
				if f.spread != "" {
					var ok bool
					fragment, ok = doc.fragments[f.spread]
					if !ok {
						panic("Unknown fragment " + f.spread)
					}
				}
				if fragment.condition == "" || kind == "" || fragment.condition == kind {
					for k, v := range store.Obj(project(value, fragment.children, depth+1)) {
						out[k] = v
					}
				}
				continue
			}
			if f.name == "__typename" {
				t := kind
				if t == "" {
					t = "Object"
				}
				out[f.alias] = t
				continue
			}
			v, ok := properties[f.name]
			if !ok {
				t := kind
				if t == "" {
					t = "Object"
				}
				panic("Cannot query field \"" + f.name + "\" on type \"" + t + "\".")
			}
			if fn, ok := v.(resolver); ok {
				v = fn(store.Obj(evaluate(f.args, vars)))
			}
			out[f.alias] = project(v, f.children, depth+1)
		}
		return out
	}
	return M{"data": project((executor{s}).root(op.kind == "mutation"), op.fields, 0)}
}
