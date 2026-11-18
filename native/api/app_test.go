package api

import (
	"encoding/json"
	"forge/store"
	"forge/transport"
	"testing"
)

func request(a *App, method, path string, body any) transport.Response {
	var b []byte
	if body != nil {
		b = store.JSON(body)
	}
	return a.Handle(&transport.Request{Method: method, URI: path, Headers: map[string]string{"host": "test", "authorization": "Bearer token"}, Body: b})
}
func decode(t *testing.T, r transport.Response) store.Map {
	t.Helper()
	var m store.Map
	if e := json.Unmarshal(r.Body, &m); e != nil {
		t.Fatal(e)
	}
	return m
}
func TestSharedStoreAndCacheInvalidation(t *testing.T) {
	a := New("token")
	r := request(a, "POST", "/api/v3/user/repos", store.Map{"name": "shared"})
	if r.Status != 201 {
		t.Fatal(string(r.Body))
	}
	repo := decode(t, r)
	q := store.Map{"query": "mutation($input: CreateIssueInput!) { created:createIssue(input:$input) { issue { id number title } } }", "variables": store.Map{"input": store.Map{"repositoryId": repo["node_id"], "title": "Quotes \" and Unicode 猫"}}}
	answer := decode(t, request(a, "POST", "/api/graphql", q))
	if answer["errors"] != nil {
		t.Fatal(answer)
	}
	path := "/api/v3/repos/mubudot/shared/issues/1"
	r = request(a, "GET", path, nil)
	if r.Status != 200 || decode(t, r)["title"] != "Quotes \" and Unicode 猫" {
		t.Fatal(string(r.Body))
	}
	request(a, "PATCH", path, store.Map{"title": "Changed", "state": "closed"})
	request(a, "POST", path+"/comments", store.Map{"body": "comment"})
	v := decode(t, request(a, "GET", path, nil))
	if v["title"] != "Changed" || v["state"] != "closed" || v["comments"] != float64(1) {
		t.Fatal(v)
	}
	request(a, "DELETE", "/api/v3/repos/mubudot/shared", nil)
	request(a, "POST", "/api/v3/user/repos", store.Map{"name": "shared"})
	if request(a, "GET", path, nil).Status != 404 {
		t.Fatal("stale repository cache")
	}
}
func TestGraphQLFragmentsAndDirectives(t *testing.T) {
	a := New("token")
	request(a, "POST", "/api/v3/user/repos", store.Map{"name": "parser"})
	q := `query Read($show:Boolean=true){ first:repository(owner:"mubudot",name:"parser"){...RepoFields} viewer @include(if:$show){login} hidden:viewer @skip(if:true){login} } fragment RepoFields on Repository {id name}`
	v := decode(t, request(a, "POST", "/api/graphql", store.Map{"query": q}))
	if v["errors"] != nil {
		t.Fatal(v)
	}
	d := store.Obj(v["data"])
	if d["hidden"] != nil || store.Obj(d["first"])["name"] != "parser" || store.Obj(d["viewer"])["login"] != "mubudot" {
		t.Fatal(v)
	}
	v = decode(t, request(a, "POST", "/api/graphql", store.Map{"query": "{viewer{unknownField}}"}))
	if v["errors"] == nil {
		t.Fatal("unknown field accepted")
	}
}
func TestAuthAndMalformedRequests(t *testing.T) {
	a := New("token")
	r := a.Handle(&transport.Request{Method: "GET", URI: "/api/v3/user", Headers: map[string]string{}})
	if r.Status != 401 {
		t.Fatal(r.Status)
	}
	for _, path := range []string{"/mcp", "/api/v3/user/repos"} {
		r = a.Handle(&transport.Request{Method: "POST", URI: path, Headers: map[string]string{"authorization": "Bearer token"}, Body: []byte("{broken")})
		if r.Status != 400 {
			t.Fatal(r.Status)
		}
		v := decode(t, r)
		if path == "/mcp" && store.Obj(v["error"])["code"] != float64(-32700) {
			t.Fatal(v)
		}
	}
}

func BenchmarkCachedIssueRead(b *testing.B) {
	a := New("token")
	request(a, "POST", "/api/v3/user/repos", store.Map{"name": "read"})
	request(a, "POST", "/api/v3/repos/mubudot/read/issues", store.Map{"title": "a small issue", "body": "cached JSON"})
	req := transport.Request{Method: "GET", URI: "/api/v3/repos/mubudot/read/issues/1", Headers: map[string]string{"host": "test", "authorization": "Bearer token"}}
	a.Handle(&req)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		a.Handle(&req)
	}
}
