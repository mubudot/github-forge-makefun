package git

import (
	"bytes"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

type Repository struct {
	DB             *Database
	Refs           map[string]string
	DefaultBranch  string
	FetchCache     map[string][]byte
	advertisements map[string][]byte
	reports        map[string][]byte
}

func NewRepository() *Repository {
	return &Repository{DB: NewDatabase(), Refs: map[string]string{}, DefaultBranch: "main", FetchCache: map[string][]byte{}, advertisements: map[string][]byte{}, reports: map[string][]byte{}}
}
func (r *Repository) Invalidate()               { clear(r.FetchCache); clear(r.advertisements) }
func (r *Repository) UpdateRef(ref, oid string) { r.Refs[ref] = oid; r.Invalidate() }
func Pkt(data string) []byte                    { return Packet([]byte(data)) }
func Packet(data []byte) []byte {
	if len(data) > 65516 {
		panic("Pkt-line too large")
	}
	out := make([]byte, 4, len(data)+4)
	hex := "0123456789abcdef"
	n := len(data) + 4
	for i := 3; i >= 0; i-- {
		out[i] = hex[n&15]
		n >>= 4
	}
	return append(out, data...)
}
func ParsePackets(data []byte, stop bool) (lines []string, offset int) {
	for offset < len(data) {
		if offset+4 > len(data) {
			panic("Invalid pkt-line")
		}
		n, e := strconv.ParseUint(string(data[offset:offset+4]), 16, 16)
		if e != nil {
			panic("Invalid pkt-line")
		}
		offset += 4
		if n == 0 {
			if stop {
				break
			}
			continue
		}
		if n == 1 || n == 2 {
			continue
		}
		if n < 4 || n > 65520 || offset+int(n)-4 > len(data) {
			panic("Invalid pkt-line size")
		}
		lines = append(lines, strings.TrimSuffix(string(data[offset:offset+int(n)-4]), "\n"))
		offset += int(n) - 4
	}
	return
}
func sideband(out, data []byte) []byte {
	for len(data) > 0 {
		n := min(len(data), 65515)
		out = append(out, Packet(append([]byte{1}, data[:n]...))...)
		data = data[n:]
	}
	return append(out, "0000"...)
}

var v2Advertisement = bytes.Join([][]byte{Pkt("version 2\n"), Pkt("ls-refs=unborn\n"), Pkt("fetch=shallow\n"), []byte("0000")}, nil)

func refNames(r *Repository) []string {
	names := make([]string, 0, len(r.Refs))
	for n := range r.Refs {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
func Advertise(r *Repository, service string, v2 bool) []byte {
	if service == "git-upload-pack" && v2 {
		return v2Advertisement
	}
	if b := r.advertisements[service]; b != nil {
		return b
	}
	head := "refs/heads/" + r.DefaultBranch
	caps := "report-status side-band-64k delete-refs"
	names := refNames(r)
	if service == "git-upload-pack" {
		caps = "side-band-64k shallow symref=HEAD:" + head
		if r.Refs[head] != "" {
			names = append([]string{"HEAD"}, names...)
		}
	}
	out := append(Pkt("# service="+service+"\n"), "0000"...)
	if len(names) == 0 {
		out = append(out, Pkt(Zero+" capabilities^{}\x00"+caps+"\n")...)
	}
	for i, name := range names {
		oid := r.Refs[name]
		if name == "HEAD" {
			oid = r.Refs[head]
		}
		line := oid + " " + name
		if i == 0 {
			line += "\x00" + caps
		}
		out = append(out, Pkt(line+"\n")...)
		if service == "git-upload-pack" && r.DB.Get(oid).Type == "tag" {
			out = append(out, Pkt(r.DB.Peel(oid)+" "+name+"^{}\n")...)
		}
	}
	out = append(out, "0000"...)
	r.advertisements[service] = out
	return out
}
func contains(lines []string, s string) bool {
	for _, l := range lines {
		if l == s {
			return true
		}
	}
	return false
}
func lsRefs(r *Repository, lines []string) []byte {
	prefixes := []string{}
	for _, l := range lines {
		if strings.HasPrefix(l, "ref-prefix ") {
			prefixes = append(prefixes, l[11:])
		}
	}
	keep := func(s string) bool {
		if len(prefixes) == 0 {
			return true
		}
		for _, p := range prefixes {
			if strings.HasPrefix(s, p) {
				return true
			}
		}
		return false
	}
	out := []byte{}
	head := "refs/heads/" + r.DefaultBranch
	sym := ""
	if contains(lines, "symrefs") {
		sym = " symref-target:" + head
	}
	if keep("HEAD") {
		if tip := r.Refs[head]; tip != "" {
			out = append(out, Pkt(tip+" HEAD"+sym+"\n")...)
		} else if contains(lines, "unborn") {
			out = append(out, Pkt("unborn HEAD"+sym+"\n")...)
		}
	}
	for _, name := range refNames(r) {
		if !keep(name) {
			continue
		}
		oid := r.Refs[name]
		peeled := ""
		if contains(lines, "peel") && r.DB.Get(oid).Type == "tag" {
			peeled = " peeled:" + r.DB.Peel(oid)
		}
		out = append(out, Pkt(oid+" "+name+peeled+"\n")...)
	}
	return append(out, "0000"...)
}
func UploadPack(r *Repository, body []byte, v2 bool) []byte {
	key := strconv.FormatBool(v2) + string(body)
	if b := r.FetchCache[key]; b != nil {
		return b
	}
	lines, _ := ParsePackets(body, false)
	if contains(lines, "command=ls-refs") {
		b := lsRefs(r, lines)
		cacheFetch(r, key, b)
		return b
	}
	wants, haves, shallow := []string{}, []string{}, []string{}
	wantSet := map[string]bool{}
	depth := 0
	for _, l := range lines {
		switch {
		case strings.HasPrefix(l, "want "):
			oid := strings.Fields(l)[1]
			if !wantSet[oid] {
				wants = append(wants, oid)
				wantSet[oid] = true
			}
		case strings.HasPrefix(l, "have "):
			haves = append(haves, l[5:])
		case strings.HasPrefix(l, "shallow "):
			shallow = append(shallow, l[8:])
		case strings.HasPrefix(l, "deepen "):
			depth, _ = strconv.Atoi(l[7:])
		}
	}
	if depth < 0 {
		depth = 0
	}
	if depth > 100000 {
		panic("Invalid fetch depth")
	}
	for _, oid := range wants {
		r.DB.Get(oid)
	}
	shallowSet := map[string]bool{}
	for _, oid := range shallow {
		shallowSet[oid] = true
	}
	held := map[string]bool{}
	for _, oid := range r.DB.Reachable(haves, 0, shallowSet) {
		held[oid] = true
	}
	wanted := r.DB.Reachable(wants, depth, nil)
	wantedSet := map[string]bool{}
	for _, oid := range wanted {
		wantedSet[oid] = true
	}
	boundary := map[string]bool{}
	order := []string{}
	if depth > 0 {
		type step struct {
			oid   string
			level int
		}
		queue := []step{}
		for _, oid := range wants {
			queue = append(queue, step{r.DB.Peel(oid), 1})
		}
		levels := map[string]int{}
		for i := 0; i < len(queue); i++ {
			s := queue[i]
			if l := levels[s.oid]; l > 0 && l <= s.level {
				continue
			}
			levels[s.oid] = s.level
			if r.DB.Get(s.oid).Type != "commit" {
				continue
			}
			parents := r.DB.Commit(s.oid).Parents
			if s.level == depth && len(parents) > 0 {
				if !boundary[s.oid] {
					order = append(order, s.oid)
				}
				boundary[s.oid] = true
			} else {
				for _, p := range parents {
					queue = append(queue, step{p, s.level + 1})
				}
			}
		}
		for oid := range boundary {
			if levels[oid] < depth {
				delete(boundary, oid)
			}
		}
	}
	info := []byte{}
	for _, oid := range order {
		if boundary[oid] {
			info = append(info, Pkt("shallow "+oid+"\n")...)
		}
	}
	for _, oid := range shallow {
		if wantedSet[oid] && !boundary[oid] {
			info = append(info, Pkt("unshallow "+oid+"\n")...)
		}
	}
	objects := []*Object{}
	for _, oid := range wanted {
		if !held[oid] {
			objects = append(objects, r.DB.Get(oid))
		}
	}
	pack := WritePack(objects)
	out := []byte{}
	if v2 {
		if len(info) > 0 {
			out = append(out, Pkt("shallow-info\n")...)
			out = append(out, info...)
			out = append(out, "0001"...)
		}
		out = append(out, Pkt("packfile\n")...)
	} else {
		if len(info) > 0 {
			out = append(out, info...)
			out = append(out, "0000"...)
		}
		out = append(out, Pkt("NAK\n")...)
	}
	out = sideband(out, pack)
	cacheFetch(r, key, out)
	return out
}
func cacheFetch(r *Repository, key string, b []byte) {
	if len(r.FetchCache) >= 16 {
		clear(r.FetchCache)
	}
	r.FetchCache[key] = b
}
func ReceivePack(r *Repository, body []byte) []byte {
	lines, offset := ParsePackets(body, true)
	caps := ""
	if len(lines) > 0 {
		_, caps, _ = strings.Cut(lines[0], "\x00")
	}
	type command struct{ old, next, ref string }
	commands := []command{}
	for _, line := range lines {
		line, _, _ = strings.Cut(line, "\x00")
		f := strings.Fields(line)
		if len(f) != 3 || !IsOID(f[0]) || !IsOID(f[1]) || !(strings.HasPrefix(f[2], "refs/heads/") || strings.HasPrefix(f[2], "refs/tags/")) || strings.ContainsRune(f[2], 0) {
			panic("Invalid ref command")
		}
		commands = append(commands, command{f[0], f[1], f[2]})
	}
	unpack := "ok"
	if offset < len(body) {
		if packError(func() { ReadPack(body[offset:], r.DB) }) != "" {
			unpack = "invalid pack"
		}
	}
	reports := []string{"unpack " + unpack + "\n"}
	for _, c := range commands {
		current := r.Refs[c.ref]
		if current == "" {
			current = Zero
		}
		reason := ""
		switch {
		case unpack != "ok":
			reason = "unpacker error"
		case current != c.old:
			reason = "stale info"
		case c.next != Zero && r.DB.Objects[c.next] == nil:
			reason = "missing object"
		case current != Zero && c.next != Zero && !r.DB.IsAncestor(current, c.next):
			reason = "non-fast-forward"
		}
		if reason != "" {
			reports = append(reports, fmt.Sprintf("ng %s %s\n", c.ref, reason))
		} else {
			if c.next == Zero {
				delete(r.Refs, c.ref)
			} else {
				r.Refs[c.ref] = c.next
			}
			r.Invalidate()
			reports = append(reports, "ok "+c.ref+"\n")
		}
	}
	banded := strings.Contains(caps, "side-band-64k")
	key := strconv.FormatBool(banded) + strings.Join(reports, "")
	if b := r.reports[key]; b != nil {
		return b
	}
	out := []byte{}
	for _, line := range reports {
		out = append(out, Pkt(line)...)
	}
	out = append(out, "0000"...)
	if banded {
		out = sideband(nil, out)
	}
	if len(r.reports) >= 128 {
		clear(r.reports)
	}
	r.reports[key] = out
	return out
}
