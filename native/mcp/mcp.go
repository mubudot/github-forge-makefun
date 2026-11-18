// Package mcp implements JSON-RPC discovery and shared-store forge tools.
package mcp

import (
	"forge/store"
	"forge/transport"
	"math"
	"strings"
)

type M = store.Map

var Revisions = []string{"2026-07-28", "2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}
var server = M{"name": "forge", "version": "1.0.0"}
var capabilities = M{"tools": M{"listChanged": false}}

type tool struct {
	name, description string
	properties        M
	required          []string
}

var str = M{"type": "string"}

func props(extra M) M {
	p := M{"owner": str, "repo": str}
	for k, v := range extra {
		p[k] = v
	}
	return p
}

var tools = []tool{
	{"create_repo", "Create a repository owned by the authenticated user.", M{"name": str, "description": str, "private": M{"type": "boolean"}}, []string{"name"}},
	{"get_repo", "Get repository details by owner and repository name.", props(nil), []string{"owner", "repo"}},
	{"create_issue", "Open an issue in a repository.", props(M{"title": str, "body": str}), []string{"owner", "repo", "title"}},
	{"list_issues", "List repository issues filtered by open, closed, or all states.", props(M{"state": M{"type": "string", "enum": []any{"open", "closed", "all"}}}), []string{"owner", "repo"}},
	{"create_pull_request", "Open a pull request from a head branch into a base branch.", props(M{"title": str, "body": str, "head": str, "base": str}), []string{"owner", "repo", "title", "head", "base"}},
	{"merge_pull_request", "Squash merge a pull request into its base branch.", props(M{"pull_number": M{"type": "integer"}}), []string{"owner", "repo", "pull_number"}},
}

func toolList() []any {
	out := []any{}
	for _, t := range tools {
		out = append(out, M{"name": t.name, "description": t.description, "inputSchema": M{"type": "object", "properties": t.properties, "required": t.required, "additionalProperties": false}})
	}
	return out
}

var toolViews = toolList()

func Error(id any, code int, message string, data any) M {
	err := M{"code": code, "message": message}
	if data != nil {
		err["data"] = data
	}
	return M{"jsonrpc": "2.0", "id": id, "error": err}
}
func revisionOK(v string) bool {
	for _, r := range Revisions {
		if v == r {
			return true
		}
	}
	return false
}
func Handle(s *store.Store, request any, headers map[string]string) transport.Response {
	req, ok := request.(map[string]any)
	method, methodOK := req["method"].(string)
	if !ok || req["jsonrpc"] != "2.0" || !methodOK {
		return transport.JSON(400, Error(req["id"], -32600, "Invalid Request", nil))
	}
	id := req["id"]
	params := store.Obj(req["params"])
	rpcError := func(code int, message string, data any) transport.Response {
		return transport.JSON(200, Error(id, code, message, data))
	}
	success := func(result any) transport.Response {
		return transport.JSON(200, M{"jsonrpc": "2.0", "id": id, "result": result})
	}
	revision := headers["mcp-protocol-version"]
	if revision == "" {
		revision = store.Str(store.Obj(params["_meta"])["io.modelcontextprotocol/protocolVersion"])
	}
	if revision != "" && !revisionOK(revision) {
		return rpcError(-32022, "Unsupported protocol version", M{"supported": Revisions})
	}
	if h := headers["mcp-method"]; h != "" && h != method {
		return rpcError(-32020, "Mcp-Method does not match request method", nil)
	}
	if strings.HasPrefix(method, "notifications/") {
		return transport.JSON(202, nil)
	}
	if _, ok := req["id"]; !ok {
		return transport.JSON(202, nil)
	}
	switch method {
	case "server/discover":
		return success(M{"resultType": "server/discover", "supportedVersions": Revisions, "capabilities": capabilities, "serverInfo": server, "_meta": M{"io.modelcontextprotocol/serverInfo": server}})
	case "initialize":
		revision := store.Str(params["protocolVersion"])
		if !revisionOK(revision) {
			revision = Revisions[0]
		}
		return success(M{"protocolVersion": revision, "capabilities": capabilities, "serverInfo": server})
	case "ping":
		return success(M{})
	case "tools/list":
		return success(M{"tools": toolViews})
	case "tools/call":
	default:
		return rpcError(-32601, "Method not found", nil)
	}
	var selected *tool
	for i := range tools {
		if tools[i].name == params["name"] {
			selected = &tools[i]
			break
		}
	}
	if selected == nil {
		return rpcError(-32602, "Unknown tool", nil)
	}
	args, ok := params["arguments"].(map[string]any)
	if !ok {
		return rpcError(-32602, "arguments must be an object", nil)
	}
	for _, key := range selected.required {
		if args[key] == nil || args[key] == "" {
			return rpcError(-32602, "Missing required argument: "+key, nil)
		}
	}
	for key, value := range args {
		property, ok := selected.properties[key]
		if !ok {
			return rpcError(-32602, "Unknown argument: "+key, nil)
		}
		p := store.Obj(property)
		valid := false
		switch p["type"] {
		case "string":
			_, valid = value.(string)
		case "boolean":
			_, valid = value.(bool)
		case "integer":
			if f, ok := value.(float64); ok {
				valid = math.Trunc(f) == f && math.Abs(f) <= 9007199254740991
			}
		}
		if !valid {
			return rpcError(-32602, "Invalid type for "+key, nil)
		}
		if enum, ok := p["enum"].([]any); ok {
			valid = false
			for _, v := range enum {
				if v == value {
					valid = true
				}
			}
			if !valid {
				return rpcError(-32602, "Invalid value for "+key, nil)
			}
		}
	}
	return success(call(s, *selected, args))
}
func call(s *store.Store, t tool, args M) (reply any) {
	defer func() {
		if e := recover(); e != nil {
			reply = M{"content": []any{M{"type": "text", "text": store.Message(e)}}, "isError": true}
		}
	}()
	var result any
	if t.name == "create_repo" {
		result = s.RepoView(s.CreateRepo(args))
	} else {
		r := s.RequireRepo(store.Str(args["owner"]), store.Str(args["repo"]))
		switch t.name {
		case "get_repo":
			result = s.RepoView(r)
		case "create_issue":
			result = s.IssueView(s.CreateIssue(r, args))
		case "list_issues":
			out := []any{}
			for _, i := range s.ListIssues(r, args) {
				out = append(out, s.IssueView(i))
			}
			result = M{"issues": out}
		case "create_pull_request":
			result = s.PullView(r, s.CreatePull(r, args))
		case "merge_pull_request":
			p := r.Pulls[store.Num(args["pull_number"])]
			if p == nil {
				panic("Pull request not found")
			}
			result = s.MergePull(r, p, nil)
		}
	}
	return M{"content": []any{M{"type": "text", "text": string(store.JSON(result))}}, "structuredContent": result, "isError": false}
}
