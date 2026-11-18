// Package git implements Git objects, packs, and the smart HTTP wire protocols.
package git

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

const Zero = "0000000000000000000000000000000000000000"

type Object struct {
	Type         string
	Data, Packed []byte
}
type TreeEntry struct{ Mode, Name, OID string }
type Commit struct {
	Tree                       string
	Parents                    []string
	Author, Committer, Message string
}
type Database struct {
	Objects map[string]*Object
	commits map[string]*Commit
	trees   map[string][]TreeEntry
}

func NewDatabase() *Database {
	return &Database{map[string]*Object{}, map[string]*Commit{}, map[string][]TreeEntry{}}
}
func IsOID(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func HashObject(t string, data []byte) string {
	h := sha1.New()
	h.Write([]byte(t + " " + strconv.Itoa(len(data)) + "\x00"))
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}
func (d *Database) Put(t string, data []byte) string {
	oid := HashObject(t, data)
	if d.Objects[oid] == nil {
		d.Objects[oid] = &Object{Type: t, Data: data}
	}
	return oid
}
func (d *Database) Get(oid string) *Object {
	o := d.Objects[oid]
	if o == nil {
		panic("Missing Git object")
	}
	return o
}
func (d *Database) Commit(oid string) *Commit {
	if c := d.commits[oid]; c != nil {
		return c
	}
	p := strings.SplitN(string(d.Get(oid).Data), "\n\n", 2)
	c := &Commit{Parents: []string{}}
	if len(p) > 1 {
		c.Message = p[1]
	}
	for _, line := range strings.Split(p[0], "\n") {
		k, v, _ := strings.Cut(line, " ")
		switch k {
		case "tree":
			c.Tree = v
		case "parent":
			c.Parents = append(c.Parents, v)
		case "author":
			c.Author = v
		case "committer":
			c.Committer = v
		}
	}
	if !IsOID(c.Tree) {
		panic("Invalid commit tree")
	}
	d.commits[oid] = c
	return c
}
func (d *Database) Tree(oid string) []TreeEntry {
	if v, ok := d.trees[oid]; ok {
		return v
	}
	data := d.Get(oid).Data
	entries := []TreeEntry{}
	for len(data) > 0 {
		sp := bytes.IndexByte(data, ' ')
		nul := bytes.IndexByte(data, 0)
		if sp < 1 || nul <= sp || nul+21 > len(data) {
			panic("Invalid tree")
		}
		entries = append(entries, TreeEntry{string(data[:sp]), string(data[sp+1 : nul]), hex.EncodeToString(data[nul+1 : nul+21])})
		data = data[nul+21:]
	}
	d.trees[oid] = entries
	return entries
}
func (d *Database) Peel(oid string) string {
	seen := map[string]bool{}
	for d.Get(oid).Type == "tag" {
		if seen[oid] {
			panic("Tag cycle")
		}
		seen[oid] = true
		s := string(d.Get(oid).Data)
		oid = ""
		for _, line := range strings.Split(s, "\n") {
			if strings.HasPrefix(line, "object ") {
				oid = strings.TrimPrefix(line, "object ")
				break
			}
		}
	}
	return oid
}
func (d *Database) WriteTree(entries []TreeEntry) string {
	sort.Slice(entries, func(i, j int) bool {
		a, b := entries[i].Name, entries[j].Name
		if entries[i].Mode == "40000" {
			a += "/"
		}
		if entries[j].Mode == "40000" {
			b += "/"
		}
		return a < b
	})
	data := []byte{}
	for _, e := range entries {
		data = append(data, []byte(e.Mode+" "+e.Name+"\x00")...)
		oid, err := hex.DecodeString(e.OID)
		if err != nil || len(oid) != 20 {
			panic("Invalid tree OID")
		}
		data = append(data, oid...)
	}
	return d.Put("tree", data)
}
func (d *Database) WriteCommit(tree string, parents []string, message string, epoch int64) string {
	id := fmt.Sprintf("Ethan Moore <mubudot@users.noreply.github.com> %d +0000", epoch)
	var b strings.Builder
	b.WriteString("tree " + tree + "\n")
	for _, p := range parents {
		b.WriteString("parent " + p + "\n")
	}
	b.WriteString("author " + id + "\ncommitter " + id + "\n\n" + strings.TrimSuffix(message, "\n") + "\n")
	return d.Put("commit", []byte(b.String()))
}
func (d *Database) Files(commit string) map[string]TreeEntry {
	out := map[string]TreeEntry{}
	if commit == "" {
		return out
	}
	var visit func(string, string)
	visit = func(tree, prefix string) {
		for _, e := range d.Tree(tree) {
			if e.Mode == "40000" || e.Mode == "040000" {
				visit(e.OID, prefix+e.Name+"/")
			} else {
				out[prefix+e.Name] = e
			}
		}
	}
	visit(d.Commit(d.Peel(commit)).Tree, "")
	return out
}
func (d *Database) TreeFromFiles(files map[string]TreeEntry) string {
	type dir struct {
		entries  []TreeEntry
		children map[string]*dir
	}
	root := &dir{children: map[string]*dir{}}
	for path, e := range files {
		parts := strings.Split(path, "/")
		at := root
		for _, p := range parts[:len(parts)-1] {
			if at.children[p] == nil {
				at.children[p] = &dir{children: map[string]*dir{}}
			}
			at = at.children[p]
		}
		e.Name = parts[len(parts)-1]
		at.entries = append(at.entries, e)
	}
	var write func(*dir) string
	write = func(at *dir) string {
		entries := append([]TreeEntry{}, at.entries...)
		for name, child := range at.children {
			entries = append(entries, TreeEntry{"40000", name, write(child)})
		}
		return d.WriteTree(entries)
	}
	return write(root)
}
func (d *Database) IsAncestor(ancestor, tip string) bool {
	todo := []string{tip}
	seen := map[string]bool{}
	for len(todo) > 0 {
		oid := todo[len(todo)-1]
		todo = todo[:len(todo)-1]
		if oid == ancestor {
			return true
		}
		if seen[oid] {
			continue
		}
		seen[oid] = true
		if o := d.Objects[oid]; o != nil {
			if o.Type == "commit" {
				todo = append(todo, d.Commit(oid).Parents...)
			} else if o.Type == "tag" {
				todo = append(todo, d.Peel(oid))
			}
		}
	}
	return false
}
func (d *Database) MergeBase(a, b string) string {
	seen := map[string]bool{}
	todo := []string{a}
	for len(todo) > 0 {
		oid := todo[len(todo)-1]
		todo = todo[:len(todo)-1]
		if oid == "" || seen[oid] {
			continue
		}
		seen[oid] = true
		todo = append(todo, d.Commit(oid).Parents...)
	}
	queue := []string{b}
	visited := map[string]bool{}
	for i := 0; i < len(queue); i++ {
		oid := queue[i]
		if seen[oid] {
			return oid
		}
		if oid == "" || visited[oid] {
			continue
		}
		visited[oid] = true
		queue = append(queue, d.Commit(oid).Parents...)
	}
	return ""
}

// Reachable preserves traversal order so packs are reproducible across map seeds.
func (d *Database) Reachable(roots []string, depth int, shallow map[string]bool) []string {
	type step struct {
		oid   string
		level int
	}
	todo := []step{}
	for _, oid := range roots {
		todo = append(todo, step{oid, 1})
	}
	seen := map[string]bool{}
	best := map[string]int{}
	out := []string{}
	for len(todo) > 0 {
		at := todo[len(todo)-1]
		todo = todo[:len(todo)-1]
		o := d.Objects[at.oid]
		if o == nil {
			continue
		}
		if seen[at.oid] && (o.Type != "commit" || best[at.oid] <= at.level) {
			continue
		}
		if !seen[at.oid] {
			out = append(out, at.oid)
		}
		seen[at.oid] = true
		switch o.Type {
		case "commit":
			best[at.oid] = at.level
			c := d.Commit(at.oid)
			todo = append(todo, step{c.Tree, at.level})
			if (depth == 0 || at.level < depth) && !shallow[at.oid] {
				for _, p := range c.Parents {
					todo = append(todo, step{p, at.level + 1})
				}
			}
		case "tree":
			for _, e := range d.Tree(at.oid) {
				if e.Mode != "160000" {
					todo = append(todo, step{e.OID, at.level})
				}
			}
		case "tag":
			todo = append(todo, step{d.Peel(at.oid), at.level})
		}
	}
	return out
}
