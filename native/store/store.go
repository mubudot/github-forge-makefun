package store

import (
	"encoding/base64"
	"forge/git"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Identity struct {
	ID     int    `json:"id"`
	NodeID string `json:"node_id"`
}
type Label struct {
	Identity
	Name        string `json:"name"`
	Color       string `json:"color"`
	Description string `json:"description"`
}
type Comment struct {
	Identity
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}
type Issue struct {
	Identity
	Number               int
	Title, Body, State   string
	Labels               []*Label
	CreatedAt, UpdatedAt string
	ClosedAt             any
	Comments             []*Comment
	Serialized           []byte
}
type Pull struct {
	Identity
	Number                                               int
	Title, Body, State, Head, Base, CreatedAt, UpdatedAt string
	Merged                                               bool
	MergedAt, MergeCommitSHA                             any
}
type Repo struct {
	Identity
	*git.Repository
	Name, Description, CreatedAt string
	Private                      bool
	Labels                       map[string]*Label
	LabelOrder                   []*Label
	Issues                       map[int]*Issue
	Pulls                        map[int]*Pull
	NextNumber                   int
}
type NodeRecord struct {
	Kind  string
	Value any
	Repo  *Repo
}
type Store struct {
	Repos      map[string]*Repo
	RepoOrder  []*Repo
	Nodes      map[string]NodeRecord
	Login      string
	NextID     int
	Epoch      int64
	Now        string
	cachedUser Map
}

func GlobalID(kind string, id int) string {
	return base64.StdEncoding.EncodeToString([]byte(kind + ":" + strconv.Itoa(id)))
}
func New() *Store {
	t, e := time.Parse(time.RFC3339Nano, os.Getenv("MAKEFUN_TRIAL_NOW"))
	if e != nil {
		t = time.Unix(1767225600, 0)
	}
	t = t.Truncate(time.Second)
	return &Store{Repos: map[string]*Repo{}, Nodes: map[string]NodeRecord{}, Login: os.Getenv("FORGE_OWNER_LOGIN"), NextID: 1, Epoch: t.Unix(), Now: t.UTC().Format("2006-01-02T15:04:05.000Z")}
}
func (s *Store) User() Map {
	login := s.Login
	if login == "" {
		login = "mubudot"
	}
	if s.cachedUser == nil || s.cachedUser["login"] != login {
		s.cachedUser = Map{"login": login, "id": 1, "node_id": GlobalID("User", 1), "name": "Ethan Moore", "type": "User"}
	}
	return s.cachedUser
}
func (s *Store) identity(kind string, id *Identity, value any, r *Repo) {
	id.ID = s.NextID
	s.NextID++
	id.NodeID = GlobalID(kind, id.ID)
	s.Nodes[id.NodeID] = NodeRecord{kind, value, r}
}

var validName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

func (s *Store) CreateRepo(in Map) *Repo {
	name := Required(in["name"], "name")
	if !validName.MatchString(name) {
		Fail(422, "Invalid repository name")
	}
	if s.Repos[name] != nil {
		Fail(422, "Repository already exists")
	}
	r := &Repo{Repository: git.NewRepository(), Name: name, Description: Text(in, "description", ""), Private: in["private"] == true, CreatedAt: s.Now, Labels: map[string]*Label{}, Issues: map[int]*Issue{}, Pulls: map[int]*Pull{}, NextNumber: 1}
	s.identity("Repository", &r.Identity, r, nil)
	s.Repos[name] = r
	s.RepoOrder = append(s.RepoOrder, r)
	if in["auto_init"] == true {
		blob := r.DB.Put("blob", []byte("# "+name+"\n"))
		tree := r.DB.WriteTree([]git.TreeEntry{{Mode: "100644", Name: "README.md", OID: blob}})
		r.UpdateRef("refs/heads/main", r.DB.WriteCommit(tree, nil, "Initial commit", s.Epoch))
	}
	return r
}
func (s *Store) FindRepo(owner, name string) *Repo {
	r := s.Repos[name]
	if r == nil {
		return nil
	}
	if s.Login == "" {
		s.Login = owner
	}
	if s.Login != owner {
		return nil
	}
	return r
}
func (s *Store) RequireRepo(owner, name string) *Repo {
	r := s.FindRepo(owner, name)
	if r == nil {
		Fail(404, "Not Found")
	}
	return r
}
func (s *Store) RepoView(r *Repo) Map {
	owner := s.User()
	return Map{"id": r.ID, "node_id": r.NodeID, "name": r.Name, "full_name": Str(owner["login"]) + "/" + r.Name, "owner": owner, "private": r.Private, "description": r.Description, "default_branch": r.DefaultBranch, "has_issues": true, "created_at": r.CreatedAt, "updated_at": r.CreatedAt, "permissions": Map{"admin": true, "push": true, "pull": true}}
}
func (s *Store) DeleteRepo(r *Repo) {
	delete(s.Repos, r.Name)
	for i, v := range s.RepoOrder {
		if v == r {
			s.RepoOrder = append(s.RepoOrder[:i], s.RepoOrder[i+1:]...)
			break
		}
	}
	for id, n := range s.Nodes {
		if n.Value == r || n.Repo == r {
			delete(s.Nodes, id)
		}
	}
}
func (s *Store) CreateLabel(r *Repo, in Map) *Label {
	name := Required(in["name"], "name")
	if r.Labels[name] != nil {
		Fail(422, "Label already exists")
	}
	l := &Label{Name: name, Color: Text(in, "color", "ededed"), Description: Text(in, "description", "")}
	s.identity("Label", &l.Identity, l, r)
	r.Labels[name] = l
	r.LabelOrder = append(r.LabelOrder, l)
	return l
}
func (s *Store) IssueLabels(r *Repo, names any) []*Label {
	out := []*Label{}
	if names == nil {
		return out
	}
	list, ok := names.([]any)
	if !ok {
		Fail(422, "labels must be an array")
	}
	for _, v := range list {
		name := Str(v)
		if _, ok := v.(string); !ok {
			name = Str(Obj(v)["name"])
		}
		l := r.Labels[name]
		if l == nil {
			Fail(422, "Label does not exist")
		}
		out = append(out, l)
	}
	return out
}
func (s *Store) CreateIssue(r *Repo, in Map) *Issue {
	title := Required(in["title"], "title")
	labels := s.IssueLabels(r, in["labels"])
	i := &Issue{Number: r.NextNumber, Title: title, Body: Text(in, "body", ""), State: "open", Labels: labels, CreatedAt: s.Now, UpdatedAt: s.Now, Comments: []*Comment{}}
	r.NextNumber++
	s.identity("Issue", &i.Identity, i, r)
	r.Issues[i.Number] = i
	return i
}

// IssueJSON caches immutable response bytes; every issue mutation invalidates it.
// A typed view also avoids reflection over interface maps in the write workload.
type issueView struct {
	ID        int      `json:"id"`
	NodeID    string   `json:"node_id"`
	Number    int      `json:"number"`
	Title     string   `json:"title"`
	Body      string   `json:"body"`
	State     string   `json:"state"`
	Labels    []*Label `json:"labels"`
	User      Map      `json:"user"`
	CreatedAt string   `json:"created_at"`
	UpdatedAt string   `json:"updated_at"`
	ClosedAt  any      `json:"closed_at"`
	Comments  int      `json:"comments"`
}

func (s *Store) IssueView(i *Issue) any {
	return issueView{i.ID, i.NodeID, i.Number, i.Title, i.Body, i.State, i.Labels, s.User(), i.CreatedAt, i.UpdatedAt, i.ClosedAt, len(i.Comments)}
}
func (s *Store) IssueJSON(i *Issue) []byte {
	if i.Serialized == nil {
		i.Serialized = JSON(s.IssueView(i))
	}
	return i.Serialized
}
func (s *Store) PatchIssue(r *Repo, i *Issue, in Map) *Issue {
	if v, ok := in["title"]; ok {
		i.Title = Required(v, "title")
	}
	if _, ok := in["body"]; ok {
		i.Body = Text(in, "body", "")
	}
	if v, ok := in["labels"]; ok {
		i.Labels = s.IssueLabels(r, v)
	}
	if v, ok := in["state"]; ok {
		state := Str(v)
		if state != "open" && state != "closed" {
			Fail(422, "Invalid issue state")
		}
		i.State = state
		if state == "closed" {
			i.ClosedAt = s.Now
		} else {
			i.ClosedAt = nil
		}
	}
	i.UpdatedAt = s.Now
	i.Serialized = nil
	return i
}
func (s *Store) ListIssues(r *Repo, in Map) []*Issue {
	state := strings.ToLower(Text(in, "state", "open"))
	out := []*Issue{}
	for _, i := range r.Issues {
		if state == "all" || state == i.State {
			out = append(out, i)
		}
	}
	sort.Slice(out, func(a, b int) bool {
		if in["direction"] == "asc" {
			return out[a].Number < out[b].Number
		}
		return out[a].Number > out[b].Number
	})
	return out
}
func (s *Store) Comment(r *Repo, i *Issue, body string) *Comment {
	Required(body, "body")
	c := &Comment{Body: body, CreatedAt: s.Now, UpdatedAt: s.Now}
	s.identity("IssueComment", &c.Identity, c, r)
	i.Comments = append(i.Comments, c)
	i.Serialized = nil
	return c
}
func (s *Store) ResolveRef(r *Repo, ref string) string {
	if ref == "" {
		ref = r.DefaultBranch
	}
	for _, key := range []string{ref, "refs/heads/" + ref, "refs/tags/" + ref} {
		if oid := r.Refs[key]; oid != "" {
			return oid
		}
	}
	if r.DB.Objects[ref] != nil {
		return ref
	}
	return ""
}
func (s *Store) CreatePull(r *Repo, in Map) *Pull {
	title := Required(in["title"], "title")
	parts := strings.Split(Required(in["head"], "head"), ":")
	head := parts[len(parts)-1]
	base := Required(in["base"], "base")
	if s.ResolveRef(r, head) == "" || s.ResolveRef(r, base) == "" {
		Fail(422, "Head or base branch does not exist")
	}
	if head == base {
		Fail(422, "Head and base must differ")
	}
	for _, p := range r.Pulls {
		if p.Head == head && p.Base == base && p.State == "open" {
			Fail(422, "Pull request already exists")
		}
	}
	p := &Pull{Number: r.NextNumber, Title: title, Body: Text(in, "body", ""), State: "open", Head: head, Base: base, CreatedAt: s.Now, UpdatedAt: s.Now}
	r.NextNumber++
	s.identity("PullRequest", &p.Identity, p, r)
	r.Pulls[p.Number] = p
	return p
}
func (s *Store) PullView(r *Repo, p *Pull) Map {
	_, conflict := s.MergePlan(r, p)
	return Map{"id": p.ID, "node_id": p.NodeID, "number": p.Number, "title": p.Title, "body": p.Body, "state": p.State, "head": Map{"ref": p.Head, "sha": s.ResolveRef(r, p.Head)}, "base": Map{"ref": p.Base, "sha": s.ResolveRef(r, p.Base)}, "user": s.User(), "created_at": p.CreatedAt, "updated_at": p.UpdatedAt, "merged": p.Merged, "merged_at": p.MergedAt, "merge_commit_sha": p.MergeCommitSHA, "mergeable": !conflict}
}
func (s *Store) PullFiles(r *Repo, p *Pull) []any {
	head, base := s.ResolveRef(r, p.Head), s.ResolveRef(r, p.Base)
	ancestor := r.DB.MergeBase(base, head)
	old, next := r.DB.Files(ancestor), r.DB.Files(head)
	paths := map[string]bool{}
	for path := range old {
		paths[path] = true
	}
	for path := range next {
		paths[path] = true
	}
	names := []string{}
	for path := range paths {
		if old[path].OID != next[path].OID {
			names = append(names, path)
		}
	}
	sort.Strings(names)
	lines := func(e git.TreeEntry) int {
		if e.OID == "" {
			return 0
		}
		text := string(r.DB.Get(e.OID).Data)
		n := strings.Count(text, "\n") + 1
		if strings.HasSuffix(text, "\n") {
			n--
		}
		return n
	}
	out := []any{}
	for _, path := range names {
		a, b := old[path], next[path]
		status, sha := "modified", b.OID
		if a.OID == "" {
			status = "added"
		}
		if b.OID == "" {
			status = "removed"
			sha = a.OID
		}
		add, del := lines(b), lines(a)
		out = append(out, Map{"filename": path, "path": path, "status": status, "additions": add, "deletions": del, "changes": add + del, "sha": sha})
	}
	return out
}
func (s *Store) MergePlan(r *Repo, p *Pull) (map[string]git.TreeEntry, bool) {
	head, base := s.ResolveRef(r, p.Head), s.ResolveRef(r, p.Base)
	ancestor := r.DB.MergeBase(base, head)
	original, current, incoming := r.DB.Files(ancestor), r.DB.Files(base), r.DB.Files(head)
	paths := map[string]bool{}
	for path := range original {
		paths[path] = true
	}
	for path := range incoming {
		paths[path] = true
	}
	conflict := false
	for path := range paths {
		before, after := original[path].OID, incoming[path].OID
		if before == after {
			continue
		}
		existing := current[path].OID
		if existing != before && existing != after {
			conflict = true
		}
		if after != "" {
			current[path] = incoming[path]
		} else {
			delete(current, path)
		}
	}
	return current, conflict
}
func (s *Store) MergePull(r *Repo, p *Pull, in Map) Map {
	if p.Merged {
		return Map{"sha": p.MergeCommitSHA, "merged": true, "message": "Pull Request successfully merged"}
	}
	if p.State != "open" {
		Fail(405, "Pull request is closed")
	}
	files, conflict := s.MergePlan(r, p)
	if conflict {
		Fail(405, "Pull Request is not mergeable")
	}
	tree := r.DB.TreeFromFiles(files)
	base := s.ResolveRef(r, p.Base)
	sha := r.DB.WriteCommit(tree, []string{base}, Text(in, "commit_title", p.Title+" (#"+strconv.Itoa(p.Number)+")"), s.Epoch)
	r.UpdateRef("refs/heads/"+p.Base, sha)
	p.State = "closed"
	p.Merged = true
	p.MergedAt = s.Now
	p.MergeCommitSHA = sha
	return Map{"sha": sha, "merged": true, "message": "Pull Request successfully merged"}
}
