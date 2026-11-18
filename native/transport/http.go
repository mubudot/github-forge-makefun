// Package transport serves buffered HTTP/1.1 with reusable connection buffers.
// Its owner loop and comparison transports do not set per-request deadlines.
package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
)

const MaxBody = 32 << 20
const maxHeader = 64 << 10

type Request struct {
	Method, URI string
	Headers     map[string]string
	Body        []byte
	Close       bool
}
type Response struct {
	Status  int
	Type    string
	Body    []byte
	Headers map[string]string
}
type Handler func(*Request) Response

func JSON(status int, v any) Response {
	b, e := json.Marshal(v)
	if e != nil {
		b = []byte(`{"message":"Invalid response"}`)
		status = 500
	}
	return Response{Status: status, Type: "application/json; charset=utf-8", Body: b}
}

var requestID atomic.Uint64

func AppendResponse(out []byte, r Response, close bool) []byte {
	out = append(out, "HTTP/1.1 "...)
	out = strconv.AppendInt(out, int64(r.Status), 10)
	out = append(out, ' ')
	out = append(out, http.StatusText(r.Status)...)
	out = append(out, "\r\nContent-Type: "...)
	if r.Type == "" {
		r.Type = "application/json; charset=utf-8"
	}
	out = append(out, r.Type...)
	out = append(out, "\r\nContent-Length: "...)
	size := len(r.Body)
	if r.Status == 204 || r.Status == 304 {
		size = 0
	}
	out = strconv.AppendInt(out, int64(size), 10)
	// Git's binary protocol does not use REST request IDs. Avoid making its
	// clients allocate and normalize an extra header for every negotiation RPC.
	if !strings.HasPrefix(r.Type, "application/x-git-") {
		out = append(out, "\r\nX-GitHub-Request-Id: "...)
		out = strconv.AppendUint(out, requestID.Add(1), 16)
	}
	out = append(out, "\r\n"...)
	for k, v := range r.Headers {
		out = append(out, k...)
		out = append(out, ": "...)
		out = append(out, v...)
		out = append(out, "\r\n"...)
	}
	if close {
		out = append(out, "Connection: close\r\n"...)
	}
	out = append(out, "\r\n"...)
	if size > 0 {
		out = append(out, r.Body...)
	}
	return out
}

func serve(c io.ReadWriteCloser, h Handler) {
	defer c.Close()
	input := make([]byte, 8192)
	used, start := 0, 0
	out := make([]byte, 0, 4096)
	p := parser{}
	continued := false
	for {
		r, n, err := p.parse(input[start:used])
		if err != nil {
			out = AppendResponse(out, JSON(400, map[string]any{"message": "Invalid HTTP request"}), true)
			writeAll(c, out)
			return
		}
		if n > 0 {
			out = AppendResponse(out, h(&r), r.Close)
			start += n
			continued = false
			if r.Close {
				writeAll(c, out)
				return
			}
			if len(out) >= 256<<10 {
				if writeAll(c, out) != nil {
					return
				}
				out = out[:0]
			}
			continue
		}
		if len(out) > 0 {
			if writeAll(c, out) != nil {
				return
			}
			out = out[:0]
		}
		if !continued && bytes.Contains(input[start:used], []byte("\r\n\r\n")) && strings.EqualFold(p.template.Headers["expect"], "100-continue") {
			if writeAll(c, []byte("HTTP/1.1 100 Continue\r\n\r\n")) != nil {
				return
			}
			continued = true
		}
		if start > 0 {
			copy(input, input[start:used])
			used -= start
			start = 0
		}
		if used == len(input) {
			if used >= MaxBody+maxHeader {
				writeAll(c, AppendResponse(nil, JSON(413, map[string]any{"message": "Request body too large"}), true))
				return
			}
			input = append(input, make([]byte, min(len(input), MaxBody+maxHeader-used))...)
		}
		n, err = c.Read(input[used:])
		if n > 0 {
			used += n
		}
		if err != nil || n == 0 {
			return
		}
	}
}
func writeAll(w io.Writer, b []byte) error {
	for len(b) > 0 {
		n, e := w.Write(b)
		if e != nil {
			return e
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		b = b[n:]
	}
	return nil
}

// ServeNetpoll is the comparison transport, with identical parsing and responses.
func ServeNetpoll(h Handler) (int, func(), error) {
	lc := net.ListenConfig{KeepAlive: -1}
	l, e := lc.Listen(context.Background(), "tcp4", ":0")
	if e != nil {
		return 0, nil, e
	}
	go func() {
		for {
			c, e := l.Accept()
			if e != nil {
				return
			}
			// Go's TCP listener already enables TCP_NODELAY; keep-alive
			// probes are disabled at listener creation, without extra calls.
			go serve(c, h)
		}
	}()
	return l.Addr().(*net.TCPAddr).Port, func() { l.Close() }, nil
}
