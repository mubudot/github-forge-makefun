package transport

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"
)

func TestFragmentedPipelinedAndChunkedRequests(t *testing.T) {
	input := []byte("POST /one HTTP/1.1\r\nHost: test\r\nContent-Length: 3\r\n\r\n猫" + "POST /two HTTP/1.1\r\nHost: test\r\nTransfer-Encoding: chunked\r\n\r\n3;ext=yes\r\nabc\r\n2\r\nde\r\n0\r\nX-Trailer: yes\r\n\r\nGET /three HTTP/1.1\r\nHost: test\r\n\r\n")
	p := parser{}
	firstEnd := bytes.Index(input, []byte("POST /two"))
	for n := 0; n < firstEnd; n++ {
		_, used, e := p.parse(input[:n])
		if e != nil || used != 0 {
			t.Fatalf("fragment %d: %d, %v", n, used, e)
		}
	}
	r, n, e := p.parse(input)
	if e != nil || n != firstEnd || string(r.Body) != "猫" {
		t.Fatalf("first: %+v %d %v", r, n, e)
	}
	r, m, e := p.parse(input[n:])
	if e != nil || string(r.Body) != "abcde" {
		t.Fatalf("chunked: %+v %v", r, e)
	}
	r, k, e := p.parse(input[n+m:])
	if e != nil || r.URI != "/three" || n+m+k != len(input) {
		t.Fatalf("pipeline: %+v %v", r, e)
	}
}
func TestRejectAmbiguousFraming(t *testing.T) {
	for _, headers := range []string{"Content-Length: 1\r\nContent-Length: 2", "Content-Length: 1\r\nTransfer-Encoding: chunked", "Transfer-Encoding: gzip", "Content-Length: -1"} {
		p := parser{}
		_, _, e := p.parse([]byte("POST / HTTP/1.1\r\n" + headers + "\r\n\r\nx"))
		if e == nil {
			t.Fatalf("accepted %s", headers)
		}
	}
}

func TestResponseHeadersAndPersistentFraming(t *testing.T) {
	for _, contentType := range []string{"", "application/json", "application/x-git-upload-pack-advertisement", "application/x-git-receive-pack-result"} {
		frame := AppendResponse(nil, Response{Status: 200, Type: contentType, Body: []byte("body")}, false)
		r, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(frame)), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != "body" || r.ContentLength != 4 || r.Close {
			t.Fatalf("invalid persistent response: %+v, body %q, err %v", r, body, err)
		}
		wantID := contentType == "" || contentType == "application/json"
		if gotID := r.Header.Get("X-GitHub-Request-Id") != ""; gotID != wantID {
			t.Fatalf("request ID for %s: got %v, want %v", contentType, gotID, wantID)
		}
	}
}
func TestServerCoalescesPipelineAndHandlesBackpressure(t *testing.T) {
	for _, mode := range []string{"blocking", "netpoll"} {
		t.Run(mode, func(t *testing.T) {
			body := bytes.Repeat([]byte{'x'}, 256<<10)
			start := ServeBlocking
			if mode == "netpoll" {
				start = ServeNetpoll
			}
			port, closeServer, e := start(func(r *Request) Response { return Response{Status: 200, Body: body} })
			if e != nil {
				t.Fatal(e)
			}
			defer closeServer()
			c, e := net.Dial("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
			if e != nil {
				t.Fatal(e)
			}
			defer c.Close()
			go func() {
				io.WriteString(c, "GET / HTTP/1.1\r\nHost: test\r\n\r\nGET / HTTP/1.1\r\nHost: test\r\nConnection: close\r\n\r\n")
			}()
			reader := bufio.NewReader(c)
			for i := 0; i < 2; i++ {
				r, e := http.ReadResponse(reader, nil)
				if e != nil {
					t.Fatal(e)
				}
				b, e := io.ReadAll(r.Body)
				r.Body.Close()
				if e != nil || !bytes.Equal(b, body) {
					t.Fatal("response truncated or reordered")
				}
			}
		})
	}
}

func TestCachedHeadersPreserveAuthAndBodyFraming(t *testing.T) {
	p := parser{}
	for iteration := 0; iteration < 4; iteration++ {
		for _, spec := range []struct{ auth, body string }{{"Bearer a", "abc"}, {"Bearer b", "猫 more bytes"}, {"Bearer a", ""}} {
			frame := []byte("POST / HTTP/1.1\r\nHost: test\r\nAuthorization: " + spec.auth + "\r\nContent-Length: " + strconv.Itoa(len(spec.body)) + "\r\n\r\n" + spec.body)
			r, n, e := p.parse(frame)
			if e != nil || n != len(frame) || string(r.Body) != spec.body || r.Headers["authorization"] != spec.auth {
				t.Fatalf("cached headers mixed requests: %+v %v", r, e)
			}
		}
	}
}

func BenchmarkAlternatingHeaders(b *testing.B) {
	requests := [][]byte{[]byte("GET /repo.git/info/refs?service=git-receive-pack HTTP/1.1\r\nHost: test\r\nAuthorization: Bearer token\r\n\r\n"), []byte("POST /repo.git/git-receive-pack HTTP/1.1\r\nHost: test\r\nAuthorization: Bearer token\r\nContent-Length: 3\r\n\r\nabc")}
	p := parser{}
	for _, r := range requests {
		p.parse(r)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p.parse(requests[i%2])
	}
}

func TestBlockingThreadLimitUsesNetpollForExtraClients(t *testing.T) {
	port, stop, e := serveBlocking(func(r *Request) Response { return JSON(200, map[string]any{"ok": true}) }, 2)
	if e != nil {
		t.Fatal(e)
	}
	defer stop()
	clients := []net.Conn{}
	defer func() {
		for _, c := range clients {
			c.Close()
		}
	}()
	for i := 0; i < 5; i++ {
		c, e := net.Dial("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if e != nil {
			t.Fatal(e)
		}
		clients = append(clients, c)
		c.SetDeadline(time.Now().Add(5 * time.Second))
		if _, e = io.WriteString(c, "GET / HTTP/1.1\r\nHost: test\r\n\r\n"); e != nil {
			t.Fatal(e)
		}
		r, e := http.ReadResponse(bufio.NewReader(c), nil)
		if e != nil {
			t.Fatal(e)
		}
		b, e := io.ReadAll(r.Body)
		r.Body.Close()
		if e != nil || string(b) != `{"ok":true}` {
			t.Fatalf("client %d: %s %v", i, b, e)
		}
	}
}
