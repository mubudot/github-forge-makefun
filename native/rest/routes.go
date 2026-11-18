// Package rest exposes the GitHub REST v3 subset over the shared store.
package rest

import (
	"forge/git"
	"forge/store"
	"forge/transport"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

type M = store.Map
type Context struct {
	S       *store.Store
	R       *store.Repo
	Method  string
	Tail    []string
	URL     *url.URL
	Body    M
	Headers map[string]string
}

func (c *Context) reply(status int, v any) transport.Response {
	r := transport.JSON(status, v)
	r.Headers = c.Headers
	return r
}
func (c *Context) page(items []any) []any {
	q := c.URL.Query()
	size := store.Num(q.Get("per_page"))
	if size == 0 {
		size = 30
	}
	size = max(1, min(100, size))
	page := max(1, store.Num(q.Get("page")))
	if page*size < len(items) {
		u := *c.URL
		q.Set("per_page", strconv.Itoa(size))
		q.Set("page", strconv.Itoa(page+1))
		u.RawQuery = q.Encode()
		c.Headers["Link"] = "<" + u.String() + ">; rel=\"next\""
	}
	start := min(len(items), (page-1)*size)
	end := min(len(items), page*size)
	return items[start:end]
}
func Page(items []any, u *url.URL) ([]any, map[string]string) {
	c := Context{URL: u, Headers: map[string]string{}}
	return c.page(items), c.Headers
}
func CommitView(r *store.Repo, sha string) M {
	c := r.DB.Commit(sha)
	person := func(line string) M {
		name, email := "Ethan Moore", ""
		epoch := int64(1767225600)
		if at := strings.LastIndex(line, "> "); at >= 0 {
			prefix := line[:at]
			if open := strings.LastIndex(prefix, " <"); open >= 0 {
				name = prefix[:open]
				email = prefix[open+2:]
			}
			f := strings.Fields(line[at+2:])
			if len(f) > 0 {
				epoch, _ = strconv.ParseInt(f[0], 10, 64)
			}
		}
		return M{"name": name, "email": email, "date": time.Unix(epoch, 0).UTC().Format("2006-01-02T15:04:05.000Z")}
	}
	parents := []any{}
	for _, p := range c.Parents {
		parents = append(parents, M{"sha": p})
	}
	return M{"sha": sha, "commit": M{"message": strings.TrimSuffix(c.Message, "\n"), "author": person(c.Author), "committer": person(c.Committer), "tree": M{"sha": c.Tree}}, "parents": parents}
}
func Route(c *Context) (transport.Response, bool) {
	s, r, method, t, b := c.S, c.R, c.Method, c.Tail, c.Body
	if len(t) == 0 {
		switch method {
		case "GET":
			return c.reply(200, s.RepoView(r)), true
		case "PATCH":
			if v, ok := b["description"]; ok {
				r.Description = store.Str(v)
			}
			if v, ok := b["default_branch"]; ok {
				r.DefaultBranch = store.Required(v, "default_branch")
				r.Invalidate()
			}
			if v, ok := b["private"]; ok {
				r.Private = v == true
			}
			return c.reply(200, s.RepoView(r)), true
		case "DELETE":
			s.DeleteRepo(r)
			return c.reply(204, nil), true
		}
		return transport.Response{}, false
	}
	switch t[0] {
	case "contents":
		return c.contents(strings.Join(t[1:], "/")), true
	case "branches":
		if method != "GET" {
			break
		}
		if len(t) > 1 {
			name := strings.Join(t[1:], "/")
			sha := r.Refs["refs/heads/"+name]
			if sha == "" {
				store.Fail(404, "Not Found")
			}
			return c.reply(200, M{"name": name, "commit": M{"sha": sha}, "protected": false}), true
		}
		list := []any{}
		for _, name := range sortedRefs(r) {
			if strings.HasPrefix(name, "refs/heads/") {
				list = append(list, M{"name": name[11:], "commit": M{"sha": r.Refs[name]}, "protected": false})
			}
		}
		return c.reply(200, c.page(list)), true
	case "commits":
		if method != "GET" {
			break
		}
		ref := c.URL.Query().Get("sha")
		if len(t) > 1 {
			ref = strings.Join(t[1:], "/")
		}
		tip := s.ResolveRef(r, ref)
		if len(t) > 1 {
			if tip == "" {
				store.Fail(404, "Not Found")
			}
			return c.reply(200, CommitView(r, r.DB.Peel(tip))), true
		}
		queue := []string{}
		if tip != "" {
			queue = append(queue, r.DB.Peel(tip))
		}
		seen := map[string]bool{}
		list := []any{}
		for i := 0; i < len(queue); i++ {
			oid := queue[i]
			if seen[oid] {
				continue
			}
			seen[oid] = true
			list = append(list, CommitView(r, oid))
			queue = append(queue, r.DB.Commit(oid).Parents...)
		}
		return c.reply(200, c.page(list)), true
	case "git":
		if len(t) < 2 || (t[1] != "refs" && t[1] != "ref") {
			break
		}
		ref := strings.Join(t[2:], "/")
		if method == "POST" && ref == "" {
			full, sha := store.Required(b["ref"], "ref"), store.Required(b["sha"], "sha")
			if !strings.HasPrefix(full, "refs/") || !git.IsOID(sha) || r.DB.Objects[sha] == nil {
				store.Fail(422, "Invalid reference")
			}
			if r.Refs[full] != "" {
				store.Fail(422, "Reference already exists")
			}
			r.UpdateRef(full, sha)
			return c.reply(201, M{"ref": full, "object": M{"type": r.DB.Get(sha).Type, "sha": sha}}), true
		}
		if method == "GET" && ref != "" {
			full := ref
			if !strings.HasPrefix(full, "refs/") {
				full = "refs/" + full
			}
			sha := r.Refs[full]
			if sha == "" {
				store.Fail(404, "Not Found")
			}
			return c.reply(200, M{"ref": full, "object": M{"type": r.DB.Get(sha).Type, "sha": sha}}), true
		}
		if method == "DELETE" && ref != "" {
			if r.Refs["refs/"+ref] == "" {
				store.Fail(404, "Not Found")
			}
			delete(r.Refs, "refs/"+ref)
			r.Invalidate()
			return c.reply(204, nil), true
		}
	case "labels":
		if len(t) != 1 {
			break
		}
		if method == "POST" {
			return c.reply(201, s.CreateLabel(r, b)), true
		}
		if method == "GET" {
			list := []any{}
			for _, l := range r.LabelOrder {
				list = append(list, l)
			}
			return c.reply(200, c.page(list)), true
		}
	case "issues":
		if len(t) == 1 {
			switch method {
			case "POST":
				i := s.CreateIssue(r, b)
				return transport.Response{Status: 201, Body: s.IssueJSON(i)}, true
			case "GET":
				q := M{}
				for k, v := range c.URL.Query() {
					q[k] = v[0]
				}
				list := []any{}
				for _, i := range s.ListIssues(r, q) {
					list = append(list, s.IssueView(i))
				}
				return c.reply(200, c.page(list)), true
			}
			break
		}
		i := r.Issues[store.Num(t[1])]
		if i == nil {
			store.Fail(404, "Not Found")
		}
		if len(t) == 2 {
			if method == "GET" {
				return transport.Response{Status: 200, Body: s.IssueJSON(i)}, true
			}
			if method == "PATCH" {
				return c.reply(200, s.IssueView(s.PatchIssue(r, i, b))), true
			}
		}
		if len(t) == 3 && t[2] == "comments" {
			if method == "POST" {
				return c.reply(201, s.Comment(r, i, store.Str(b["body"]))), true
			}
			if method == "GET" {
				list := []any{}
				for _, comment := range i.Comments {
					list = append(list, comment)
				}
				return c.reply(200, c.page(list)), true
			}
		}
	case "pulls":
		if len(t) == 1 {
			if method == "POST" {
				return c.reply(201, s.PullView(r, s.CreatePull(r, b))), true
			}
			if method == "GET" {
				state := c.URL.Query().Get("state")
				if state == "" {
					state = "open"
				}
				pulls := []*store.Pull{}
				for _, p := range r.Pulls {
					if state == "all" || p.State == state {
						pulls = append(pulls, p)
					}
				}
				sort.Slice(pulls, func(i, j int) bool { return pulls[i].Number > pulls[j].Number })
				list := []any{}
				for _, p := range pulls {
					list = append(list, s.PullView(r, p))
				}
				return c.reply(200, c.page(list)), true
			}
			break
		}
		p := r.Pulls[store.Num(t[1])]
		if p == nil {
			store.Fail(404, "Not Found")
		}
		if len(t) == 2 && method == "GET" {
			return c.reply(200, s.PullView(r, p)), true
		}
		if len(t) == 3 {
			if t[2] == "files" && method == "GET" {
				return c.reply(200, c.page(s.PullFiles(r, p))), true
			}
			if t[2] == "merge" && method == "PUT" {
				return c.reply(200, s.MergePull(r, p, b)), true
			}
		}
	}
	return transport.Response{}, false
}
func sortedRefs(r *store.Repo) []string {
	out := []string{}
	for n := range r.Refs {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
