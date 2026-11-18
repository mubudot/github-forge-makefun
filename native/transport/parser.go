package transport

import (
	"bytes"
	"errors"
	"strconv"
	"strings"
)

type parser struct {
	lastHeader []byte
	template   Request
	chunked    bool
	length     int
	headers    [8]headerTemplate
	nextHeader int
}

type headerTemplate struct {
	bytes   []byte
	request Request
	chunked bool
	length  int
}

func (p *parser) restoreHeader(head []byte) bool {
	for _, h := range p.headers {
		if h.bytes != nil && bytes.Equal(head, h.bytes) {
			p.lastHeader, p.template, p.chunked, p.length = h.bytes, h.request, h.chunked, h.length
			return true
		}
	}
	return false
}

func (p *parser) rememberHeader(head []byte) {
	p.lastHeader = bytes.Clone(head)
	p.headers[p.nextHeader] = headerTemplate{p.lastHeader, p.template, p.chunked, p.length}
	p.nextHeader = (p.nextHeader + 1) % len(p.headers)
}

var errFrame = errors.New("invalid HTTP framing")

// parse never consumes an incomplete request. Returned body bytes are valid until
// the next read; handlers finish synchronously before that buffer can be reused.
func (p *parser) parse(data []byte) (Request, int, error) {
	at := bytes.Index(data, []byte("\r\n\r\n"))
	if at < 0 {
		if len(data) > maxHeader {
			return Request{}, 0, errFrame
		}
		return Request{}, 0, nil
	}
	if at == 0 || at > maxHeader {
		return Request{}, 0, errFrame
	}
	head := data[:at]
	if !bytes.Equal(head, p.lastHeader) && !p.restoreHeader(head) {
		lines := strings.Split(string(head), "\r\n")
		first := strings.Split(lines[0], " ")
		if len(first) != 3 || (first[2] != "HTTP/1.1" && first[2] != "HTTP/1.0") {
			return Request{}, 0, errFrame
		}
		r := Request{Method: first[0], URI: first[1], Headers: map[string]string{}, Close: first[2] == "HTTP/1.0"}
		length := -1
		for _, line := range lines[1:] {
			k, v, ok := strings.Cut(line, ":")
			if !ok || k == "" || strings.ContainsAny(k, " \t") {
				return Request{}, 0, errFrame
			}
			k = strings.ToLower(k)
			v = strings.TrimSpace(v)
			if k == "content-length" {
				n, e := strconv.Atoi(v)
				if e != nil || n < 0 || n > MaxBody || (length >= 0 && n != length) {
					return Request{}, 0, errFrame
				}
				length = n
			}
			if old, ok := r.Headers[k]; ok && k != "content-length" {
				v = old + ", " + v
			}
			r.Headers[k] = v
		}
		transfer := strings.ToLower(r.Headers["transfer-encoding"])
		if transfer != "" && (transfer != "chunked" || length >= 0) {
			return Request{}, 0, errFrame
		}
		conn := strings.ToLower(r.Headers["connection"])
		if conn == "close" {
			r.Close = true
		} else if conn == "keep-alive" {
			r.Close = false
		}
		p.chunked = transfer == "chunked"
		p.length = max(0, length)
		p.template = r
		p.rememberHeader(head)
	}
	r := p.template
	start := at + 4
	if p.chunked {
		body, n, e := decodeChunks(data[start:])
		if e != nil || n == 0 {
			return Request{}, 0, e
		}
		r.Body = body
		return r, start + n, nil
	}
	end := start + p.length
	if end > len(data) {
		return Request{}, 0, nil
	}
	r.Body = data[start:end]
	return r, end, nil
}
func decodeChunks(data []byte) ([]byte, int, error) {
	pos, total := 0, 0
	type part struct{ start, end int }
	parts := []part{}
	for {
		at := bytes.Index(data[pos:], []byte("\r\n"))
		if at < 0 {
			if len(data)-pos > maxHeader {
				return nil, 0, errFrame
			}
			return nil, 0, nil
		}
		line := string(data[pos : pos+at])
		size, _, _ := strings.Cut(line, ";")
		n, e := strconv.ParseUint(strings.TrimSpace(size), 16, 32)
		if e != nil || n > MaxBody || total+int(n) > MaxBody {
			return nil, 0, errFrame
		}
		pos += at + 2
		if n == 0 {
			for {
				at = bytes.Index(data[pos:], []byte("\r\n"))
				if at < 0 {
					return nil, 0, nil
				}
				pos += at + 2
				if at == 0 {
					out := make([]byte, 0, total)
					for _, p := range parts {
						out = append(out, data[p.start:p.end]...)
					}
					return out, pos, nil
				}
			}
		}
		end := pos + int(n)
		if end+2 > len(data) {
			return nil, 0, nil
		}
		if data[end] != '\r' || data[end+1] != '\n' {
			return nil, 0, errFrame
		}
		parts = append(parts, part{pos, end})
		total += int(n)
		pos = end + 2
	}
}
