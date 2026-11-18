// Package graphql parses and executes the forge's GraphQL API. Introspection
// and runtime field validation use the same schema definitions.
package graphql

import (
	"encoding/json"
	"forge/store"
	"strconv"
	"strings"
	"unicode"
)

type M = store.Map
type directive struct {
	name string
	args M
}
type field struct {
	name, alias, condition, spread string
	args                           M
	children                       []field
	directives                     []directive
}
type operation struct {
	kind, name string
	defaults   M
	fields     []field
}
type document struct {
	operations []operation
	fragments  map[string]field
}
type variable string
type parser struct {
	tokens     []string
	pos, depth int
}

func tokenize(source string) []string {
	if len(source) > 1<<20 {
		panic("Query too large")
	}
	out := []string{}
	for i := 0; i < len(source); {
		c := source[i]
		if c == ' ' || c == '\n' || c == '\r' || c == '\t' || c == ',' {
			i++
			continue
		}
		if c == '#' {
			for i < len(source) && source[i] != '\n' && source[i] != '\r' {
				i++
			}
			continue
		}
		start := i
		if strings.HasPrefix(source[i:], "...") {
			i += 3
		} else if c == '"' {
			i++
			closed := false
			for i < len(source) {
				if source[i] == '\\' {
					i += 2
					continue
				}
				if source[i] == '"' {
					i++
					closed = true
					break
				}
				i++
			}
			if !closed {
				panic("Invalid GraphQL token")
			}
		} else if isNameStart(c) {
			i++
			for i < len(source) && (isNameStart(source[i]) || source[i] >= '0' && source[i] <= '9') {
				i++
			}
		} else if c == '-' || c >= '0' && c <= '9' {
			i++
			for i < len(source) && (source[i] >= '0' && source[i] <= '9' || source[i] == '.') {
				i++
			}
		} else if strings.ContainsRune("!$():=@[]{}|", rune(c)) {
			i++
		} else {
			panic("Invalid GraphQL token")
		}
		out = append(out, source[start:i])
	}
	return out
}
func isNameStart(c byte) bool { return c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' }
func (p *parser) peek() string {
	if p.pos >= len(p.tokens) {
		return ""
	}
	return p.tokens[p.pos]
}
func (p *parser) take() string {
	t := p.peek()
	if t == "" {
		panic("Unexpected end of GraphQL document")
	}
	p.pos++
	return t
}
func (p *parser) expect(t string) {
	if p.take() != t {
		panic("Expected " + t)
	}
}
func (p *parser) name() string {
	t := p.take()
	if !isNameStart(t[0]) {
		panic("Expected a field name")
	}
	return t
}
func (p *parser) value() any {
	t := p.take()
	switch t {
	case "$":
		return variable(p.name())
	case "[":
		out := []any{}
		for p.peek() != "]" {
			out = append(out, p.value())
		}
		p.expect("]")
		return out
	case "{":
		out := M{}
		for p.peek() != "}" {
			name := p.name()
			p.expect(":")
			out[name] = p.value()
		}
		p.expect("}")
		return out
	case "true":
		return true
	case "false":
		return false
	case "null":
		return nil
	}
	if t[0] == '"' {
		var s string
		if json.Unmarshal([]byte(t), &s) != nil {
			panic("Invalid GraphQL string")
		}
		return s
	}
	if t[0] == '-' || unicode.IsDigit(rune(t[0])) {
		v, e := strconv.ParseFloat(t, 64)
		if e != nil {
			panic("Invalid GraphQL number")
		}
		return v
	}
	if !isNameStart(t[0]) {
		panic("Invalid GraphQL value")
	}
	return t
}
func (p *parser) args() M {
	out := M{}
	if p.peek() != "(" {
		return out
	}
	p.take()
	for p.peek() != ")" {
		name := p.name()
		p.expect(":")
		out[name] = p.value()
	}
	p.expect(")")
	return out
}
func (p *parser) directives() []directive {
	out := []directive{}
	for p.peek() == "@" {
		p.take()
		out = append(out, directive{p.name(), p.args()})
	}
	return out
}
func (p *parser) selection() []field {
	p.depth++
	if p.depth > 64 {
		panic("GraphQL selection too deep")
	}
	p.expect("{")
	out := []field{}
	for p.peek() != "}" {
		if p.peek() == "..." {
			p.take()
			name := p.name()
			f := field{args: M{}}
			if name == "on" {
				f.condition = p.name()
			} else {
				f.spread = name
			}
			f.directives = p.directives()
			if f.spread == "" {
				f.children = p.selection()
			}
			out = append(out, f)
			continue
		}
		name := p.name()
		alias := name
		if p.peek() == ":" {
			p.take()
			name = p.name()
		}
		f := field{name: name, alias: alias, args: p.args(), directives: p.directives()}
		if p.peek() == "{" {
			f.children = p.selection()
		}
		out = append(out, f)
	}
	p.expect("}")
	p.depth--
	return out
}
func parse(source string) document {
	p := parser{tokens: tokenize(source)}
	d := document{fragments: map[string]field{}}
	for p.peek() != "" {
		if p.peek() == "fragment" {
			p.take()
			name := p.name()
			p.expect("on")
			f := field{condition: p.name()}
			f.directives = p.directives()
			f.children = p.selection()
			d.fragments[name] = f
			continue
		}
		o := operation{kind: "query", defaults: M{}}
		if p.peek() != "{" {
			o.kind = p.take()
			if o.kind != "query" && o.kind != "mutation" {
				panic("Unsupported operation")
			}
			if p.peek() != "(" && p.peek() != "{" {
				o.name = p.name()
			}
			if p.peek() == "(" {
				p.take()
				for p.peek() != ")" {
					p.expect("$")
					name := p.name()
					p.expect(":")
					for p.peek() != "=" && p.peek() != "$" && p.peek() != ")" {
						p.take()
					}
					if p.peek() == "=" {
						p.take()
						o.defaults[name] = p.value()
					}
				}
				p.expect(")")
			}
		}
		p.directives()
		o.fields = p.selection()
		d.operations = append(d.operations, o)
	}
	if len(d.operations) == 0 {
		panic("GraphQL document has no operation")
	}
	return d
}
func evaluate(v any, vars M) any {
	switch x := v.(type) {
	case variable:
		return vars[string(x)]
	case []any:
		out := make([]any, len(x))
		for i, v := range x {
			out[i] = evaluate(v, vars)
		}
		return out
	case map[string]any:
		out := M{}
		for k, v := range x {
			out[k] = evaluate(v, vars)
		}
		return out
	}
	return v
}
