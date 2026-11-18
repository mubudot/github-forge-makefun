package rest

import (
	"encoding/base64"
	"forge/git"
	"forge/store"
	"forge/transport"
	"strings"
)

func contentView(r *store.Repo, path string, e git.TreeEntry) M {
	dir := e.Mode == "40000" || e.Mode == "040000"
	o := r.DB.Get(e.OID)
	parts := strings.Split(path, "/")
	v := M{"type": "file", "name": parts[len(parts)-1], "path": path, "sha": e.OID, "size": len(o.Data)}
	if dir {
		v["type"] = "dir"
		v["size"] = 0
	} else {
		v["encoding"] = "base64"
		v["content"] = base64.StdEncoding.EncodeToString(o.Data)
	}
	return v
}
func (c *Context) contents(path string) transport.Response {
	s, r, b, method := c.S, c.R, c.Body, c.Method
	branch := store.Text(b, "branch", r.DefaultBranch)
	ref := branch
	if method == "GET" {
		ref = c.URL.Query().Get("ref")
	}
	tip := s.ResolveRef(r, ref)
	if method == "GET" {
		if tip == "" {
			store.Fail(404, "Not Found")
		}
		tree := r.DB.Commit(r.DB.Peel(tip)).Tree
		parts := []string{}
		for _, p := range strings.Split(path, "/") {
			if p != "" {
				parts = append(parts, p)
			}
		}
		directory := func(tree, prefix string) transport.Response {
			out := []any{}
			for _, e := range r.DB.Tree(tree) {
				out = append(out, contentView(r, prefix+e.Name, e))
			}
			return c.reply(200, out)
		}
		if len(parts) == 0 {
			return directory(tree, "")
		}
		for i, name := range parts {
			var entry *git.TreeEntry
			for _, e := range r.DB.Tree(tree) {
				if e.Name == name {
					v := e
					entry = &v
					break
				}
			}
			if entry == nil {
				store.Fail(404, "Not Found")
			}
			dir := entry.Mode == "40000" || entry.Mode == "040000"
			if i == len(parts)-1 {
				if dir {
					return directory(entry.OID, path+"/")
				}
				return c.reply(200, contentView(r, path, *entry))
			}
			if !dir {
				store.Fail(404, "Not Found")
			}
			tree = entry.OID
		}
	}
	if method != "PUT" && method != "DELETE" {
		store.Fail(404, "Not Found")
	}
	if path == "" {
		store.Fail(422, "Invalid path")
	}
	for _, p := range strings.Split(path, "/") {
		if p == "" || p == "." || p == ".." || strings.ContainsRune(p, 0) {
			store.Fail(422, "Invalid path")
		}
	}
	if store.Str(b["branch"]) != "" && tip == "" {
		store.Fail(404, "Branch not found")
	}
	files := r.DB.Files(tip)
	old, exists := files[path]
	if method == "DELETE" && !exists {
		store.Fail(404, "Not Found")
	}
	if exists && b["sha"] != old.OID || !exists && store.Str(b["sha"]) != "" {
		store.Fail(409, "Content SHA does not match")
	}
	var content any
	if method == "DELETE" {
		delete(files, path)
	} else {
		v, ok := b["content"].(string)
		if !ok {
			store.Fail(422, "content is required")
		}
		v = strings.Join(strings.Fields(v), "")
		v = strings.NewReplacer("-", "+", "_", "/").Replace(v)
		v = strings.TrimRight(v, "=")
		data, _ := base64.RawStdEncoding.DecodeString(v)
		mode := old.Mode
		if mode == "" {
			mode = "100644"
		}
		parts := strings.Split(path, "/")
		entry := git.TreeEntry{Mode: mode, Name: parts[len(parts)-1], OID: r.DB.Put("blob", data)}
		files[path] = entry
		content = contentView(r, path, entry)
	}
	tree := r.DB.TreeFromFiles(files)
	message := store.Required(b["message"], "message")
	parents := []string{}
	parentViews := []any{}
	if tip != "" {
		parents = append(parents, tip)
		parentViews = append(parentViews, M{"sha": tip})
	}
	sha := r.DB.WriteCommit(tree, parents, message, s.Epoch)
	r.UpdateRef("refs/heads/"+branch, sha)
	status := 201
	if method == "DELETE" || exists {
		status = 200
	}
	return c.reply(status, M{"content": content, "commit": M{"sha": sha, "message": message, "tree": M{"sha": tree}, "parents": parentViews}})
}
