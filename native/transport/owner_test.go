package transport

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"net/http"
	"runtime"
	"strconv"
	"testing"
	"time"
)

func TestOwnerIdleConnectionsAllowRepeatedGC(t *testing.T) {
	old := runtime.GOMAXPROCS(2)
	defer runtime.GOMAXPROCS(old)
	port, stop, e := ServeOwner(func(*Request) Response { return Response{Status: 200, Body: []byte("ok")} })
	if e != nil {
		t.Fatal(e)
	}
	defer stop()
	var active net.Conn
	for range 20 {
		c, e := net.Dial("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if e != nil {
			t.Fatal(e)
		}
		defer c.Close()
		active = c
	}
	active.SetDeadline(time.Now().Add(10 * time.Second))
	reader := bufio.NewReader(active)
	for range 100 {
		if _, e = io.WriteString(active, "GET / HTTP/1.1\r\nHost: test\r\n\r\n"); e != nil {
			t.Fatal(e)
		}
		r, e := http.ReadResponse(reader, nil)
		if e != nil {
			t.Fatal(e)
		}
		b, e := io.ReadAll(r.Body)
		r.Body.Close()
		if e != nil || string(b) != "ok" {
			t.Fatalf("response %q: %v", b, e)
		}
		runtime.GC()
	}
}

func TestOwnerPipelineResumesAfterSocketBackpressure(t *testing.T) {
	body := bytes.Repeat([]byte("backpressure:"), 600000)
	port, stop, e := ServeOwner(func(*Request) Response { return Response{Status: 200, Body: body} })
	if e != nil {
		t.Fatal(e)
	}
	defer stop()
	c, e := net.DialTCP("tcp4", nil, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	c.SetReadBuffer(65536)
	c.SetDeadline(time.Now().Add(10 * time.Second))
	if _, e = io.WriteString(c, "GET / HTTP/1.1\r\nHost: test\r\n\r\nGET / HTTP/1.1\r\nHost: test\r\nConnection: close\r\n\r\n"); e != nil {
		t.Fatal(e)
	}
	time.Sleep(20 * time.Millisecond)
	reader := bufio.NewReader(c)
	for range 2 {
		r, e := http.ReadResponse(reader, nil)
		if e != nil {
			t.Fatal(e)
		}
		b, e := io.ReadAll(r.Body)
		r.Body.Close()
		if e != nil || !bytes.Equal(b, body) {
			t.Fatalf("large pipeline reply: %d bytes: %v", len(b), e)
		}
	}
	if _, e = reader.ReadByte(); e != io.EOF {
		t.Fatalf("close response did not close socket: %v", e)
	}
}
