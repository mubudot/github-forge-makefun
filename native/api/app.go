// Package api dispatches all four wire surfaces against one store. Requests are
// synchronous; cached reply bytes leave the lock before the socket write.
package api

import (
	"encoding/json"
	"forge/git"
	"forge/graphql"
	"forge/mcp"
	"forge/rest"
	"forge/store"
	"forge/transport"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

type route struct {
	url         *url.URL
	parts       []string
	issueNumber int
}
type App struct {
	mu                sync.Mutex
	Store             *store.Store
	token             string
	routes            map[string]route
	lastHost, lastURI string
	lastRoute         route
}

func New(token string) *App {
	return &App{Store: store.New(), token: "Bearer " + token, routes: map[string]route{}}
}
func (a *App) Handle(req *transport.Request) (response transport.Response) {
	a.mu.Lock()
	defer a.mu.Unlock()
	defer func() {
		if err := recover(); err != nil {
			if e, ok := err.(store.HTTPError); ok {
				response = transport.JSON(e.Status, store.Map{"message": e.Message})
			} else {
				response = transport.JSON(400, store.Map{"message": "Invalid request"})
			}
		}
	}()
	if req.Method == "GET" && req.URI == "/healthz" {
		return transport.JSON(200, store.Map{"ok": true})
	}
	if a.token == "Bearer " || req.Headers["authorization"] != a.token {
		store.Fail(401, "Bad credentials")
	}
	host := req.Headers["host"]
	rt := a.lastRoute
	if host != a.lastHost || req.URI != a.lastURI {
		key := host + req.URI
		var ok bool
		rt, ok = a.routes[key]
		if !ok {
			u, e := url.ParseRequestURI(req.URI)
			if e != nil {
				store.Fail(400, "Invalid URL")
			}
			u.Scheme = "http"
			u.Host = req.Headers["host"]
			parts := []string{}
			for _, p := range strings.Split(u.EscapedPath(), "/") {
				if p != "" {
					v, e := url.PathUnescape(p)
					if e != nil {
						store.Fail(400, "Invalid URL")
					}
					parts = append(parts, v)
				}
			}
			rt = route{url: u, parts: parts}
			if len(parts) == 7 && parts[0] == "api" && parts[1] == "v3" && parts[2] == "repos" && parts[5] == "issues" {
				rt.issueNumber, _ = strconv.Atoi(parts[6])
			}
			if len(a.routes) >= 512 {
				clear(a.routes)
			}
			a.routes[key] = rt
		}
		a.lastHost, a.lastURI, a.lastRoute = host, req.URI, rt
	}
	u, parts := rt.url, rt.parts
	method := req.Method
	s := a.Store
	if method == "GET" && rt.issueNumber > 0 {
		r := s.RequireRepo(parts[3], parts[4])
		i := r.Issues[rt.issueNumber]
		if i == nil {
			store.Fail(404, "Not Found")
		}
		return transport.Response{Status: 200, Body: s.IssueJSON(i)}
	}
	if len(parts) == 3 && strings.HasSuffix(parts[1], ".git") && (parts[2] == "git-upload-pack" || parts[2] == "git-receive-pack") || len(parts) == 4 && strings.HasSuffix(parts[1], ".git") && parts[2] == "info" && parts[3] == "refs" {
		r := s.RequireRepo(parts[0], strings.TrimSuffix(parts[1], ".git"))
		v2 := req.Headers["git-protocol"] == "version=2"
		service := parts[2]
		if method == "GET" && service == "info" {
			service = u.Query().Get("service")
			if service != "git-upload-pack" && service != "git-receive-pack" {
				store.Fail(400, "Invalid Git service")
			}
			return transport.Response{Status: 200, Type: "application/x-" + service + "-advertisement", Body: git.Advertise(r.Repository, service, v2)}
		}
		if method == "POST" && service != "info" {
			return gitRequest(r, service, req.Body, v2)
		}
		store.Fail(404, "Not Found")
	}
	var body any = store.Map{}
	if method == "POST" || method == "PUT" || method == "PATCH" || method == "DELETE" {
		if len(req.Body) > 0 {
			if json.Unmarshal(req.Body, &body) != nil {
				if u.Path == "/mcp" {
					return transport.JSON(400, mcp.Error(nil, -32700, "Parse error", nil))
				}
				store.Fail(400, "Malformed JSON body")
			}
		}
	}
	if u.Path == "/mcp" && method == "POST" {
		return mcp.Handle(s, body, req.Headers)
	}
	if u.Path == "/api/graphql" && method == "POST" {
		if list, ok := body.([]any); ok {
			out := make([]any, len(list))
			for i, v := range list {
				out[i] = graphql.Execute(s, v)
			}
			return transport.JSON(200, out)
		}
		return transport.JSON(200, graphql.Execute(s, body))
	}
	if (u.Path == "/api/v3" || u.Path == "/api/v3/") && method == "GET" {
		r := transport.JSON(200, store.Map{"current_user_url": "/api/v3/user", "repository_url": "/api/v3/repos/{owner}/{repo}"})
		r.Headers = map[string]string{"X-OAuth-Scopes": "repo, read:org"}
		return r
	}
	if len(parts) >= 3 && parts[0] == "api" && parts[1] == "v3" {
		b, ok := body.(map[string]any)
		if !ok {
			store.Fail(400, "Expected a JSON object")
		}
		if len(parts) == 3 && method == "GET" {
			if parts[2] == "meta" {
				return transport.JSON(200, store.Map{"installed_version": "forge-1.0.0"})
			}
			if parts[2] == "user" {
				return transport.JSON(200, s.User())
			}
		}
		if len(parts) == 4 && parts[2] == "user" && parts[3] == "repos" {
			if method == "POST" {
				return transport.JSON(201, s.RepoView(s.CreateRepo(b)))
			}
			if method == "GET" {
				list := []any{}
				for _, r := range s.RepoOrder {
					list = append(list, s.RepoView(r))
				}
				paged, h := rest.Page(list, u)
				r := transport.JSON(200, paged)
				r.Headers = h
				return r
			}
		}
		if len(parts) >= 5 && parts[2] == "repos" {
			r := s.RequireRepo(parts[3], parts[4])
			c := &rest.Context{S: s, R: r, Method: method, Tail: parts[5:], URL: u, Body: b, Headers: map[string]string{}}
			if response, ok := rest.Route(c); ok {
				return response
			}
		}
	}
	store.Fail(404, "Not Found")
	return
}
func gitRequest(r *store.Repo, service string, body []byte, v2 bool) (response transport.Response) {
	defer func() {
		if recover() != nil {
			response = transport.JSON(400, store.Map{"message": "Malformed Git request"})
		}
	}()
	var data []byte
	if service == "git-upload-pack" {
		data = git.UploadPack(r.Repository, body, v2)
	} else {
		data = git.ReceivePack(r.Repository, body)
	}
	return transport.Response{Status: 200, Type: "application/x-" + service + "-result", Body: data}
}
