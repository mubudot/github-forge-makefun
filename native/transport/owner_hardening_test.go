package transport

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestOwnerSingleProcessorWaitAllowsGC(t *testing.T) {
	previous := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previous)
	if rawWaitEnabled() {
		t.Fatal("raw wait enabled without a spare Go processor")
	}
	port, stop, e := ServeOwner(func(*Request) Response { return Response{Status: 200, Body: []byte("ok")} })
	if e != nil {
		t.Fatal(e)
	}
	defer stop()
	c := ownerClient(t, port)
	reader := bufio.NewReader(c)
	for range 25 {
		if _, e = io.WriteString(c, "GET / HTTP/1.1\r\nHost: test\r\n\r\n"); e != nil {
			t.Fatal(e)
		}
		if _, e = ownerReply(reader); e != nil {
			t.Fatal(e)
		}
		runtime.GC()
	}
}

func ownerClient(t *testing.T, port int) *net.TCPConn {
	t.Helper()
	c, e := net.DialTCP("tcp4", nil, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
	if e != nil {
		t.Fatal(e)
	}
	if e = c.SetDeadline(time.Now().Add(15 * time.Second)); e != nil {
		c.Close()
		t.Fatal(e)
	}
	t.Cleanup(func() { c.Close() })
	return c
}
func ownerReply(reader *bufio.Reader) ([]byte, error) {
	r, e := http.ReadResponse(reader, nil)
	if e != nil {
		return nil, e
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return nil, fmt.Errorf("status %d", r.StatusCode)
	}
	return io.ReadAll(r.Body)
}

func TestOwnerSetupFailuresCloseBeforeFallback(t *testing.T) {
	for failAt := 1; failAt <= 8; failAt++ {
		t.Run(strconv.Itoa(failAt), func(t *testing.T) {
			step, nextFD := 0, 10
			open := map[int]bool{}
			enter := func() error {
				step++
				if step == failAt {
					return syscall.EPERM
				}
				return nil
			}
			allocate := func() int { fd := nextFD; nextFD++; open[fd] = true; return fd }
			calls := ownerSetupCalls{
				socket: func(int, int, int) (int, error) {
					if e := enter(); e != nil {
						return -1, e
					}
					return allocate(), nil
				},
				bind:   func(int, syscall.Sockaddr) error { return enter() },
				listen: func(int, int) error { return enter() },
				address: func(int) (syscall.Sockaddr, error) {
					if e := enter(); e != nil {
						return nil, e
					}
					return &syscall.SockaddrInet4{Port: 123}, nil
				},
				epoll: func(int) (int, error) {
					if e := enter(); e != nil {
						return -1, e
					}
					return allocate(), nil
				},
				pair: func(int, int, int) ([2]int, error) {
					if e := enter(); e != nil {
						return [2]int{-1, -1}, e
					}
					return [2]int{allocate(), allocate()}, nil
				},
				control: func(int, int, int, uint32, int32) error { return enter() },
				close: func(fd int) error {
					if !open[fd] {
						t.Errorf("closed unknown/already closed fd %d", fd)
					}
					delete(open, fd)
					return nil
				},
			}
			called, stopped := false, false
			port, stop, e := serveOwnerWithFallback(func(*Request) Response { return Response{} }, calls, func(Handler) (int, func(), error) {
				called = true
				if len(open) != 0 {
					t.Errorf("fallback started before fd cleanup: %v", open)
				}
				return 321, func() { stopped = true }, nil
			})
			if e != nil || port != 321 || !called || stop == nil {
				t.Fatalf("fallback: %d %v %v", port, called, e)
			}
			stop()
			if !stopped {
				t.Fatal("fallback close function lost")
			}
			if len(open) != 0 {
				t.Fatalf("leaked fds: %v", open)
			}
		})
	}
}

func TestOwnerSetupFailureServesWithExistingTransport(t *testing.T) {
	calls := linuxOwnerSetup
	calls.epoll = func(int) (int, error) { return -1, syscall.EPERM }
	port, stop, e := serveOwnerWithFallback(func(*Request) Response { return Response{Status: 200, Body: []byte("fallback")} }, calls, ServeBlocking)
	if e != nil {
		t.Fatal(e)
	}
	defer stop()
	c := ownerClient(t, port)
	io.WriteString(c, "GET / HTTP/1.1\r\nHost: test\r\nConnection: close\r\n\r\n")
	b, e := ownerReply(bufio.NewReader(c))
	if e != nil || string(b) != "fallback" {
		t.Fatalf("fallback reply %q: %v", b, e)
	}
}

func TestOwnerHalfCloseBackpressureDoesNotStayReady(t *testing.T) {
	s, e := newOwnerServer(linuxOwnerSetup)
	if e != nil {
		t.Fatal(e)
	}
	defer s.finish()
	pair, e := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC|syscall.SOCK_NONBLOCK, 0)
	if e != nil {
		t.Fatal(e)
	}
	defer syscall.Close(pair[1])
	if e = syscall.SetsockoptInt(pair[0], syscall.SOL_SOCKET, syscall.SO_SNDBUF, 4096); e != nil {
		t.Fatal(e)
	}
	c := &ownerConn{fd: pair[0], serial: 1, input: make([]byte, 8192), out: make([]byte, 0, 4096), interest: syscall.EPOLLIN | syscall.EPOLLRDHUP}
	s.connections[c.fd] = c
	if e = ownerControl(s.epfd, syscall.EPOLL_CTL_ADD, c.fd, c.interest, c.serial); e != nil {
		t.Fatal(e)
	}
	if _, e = syscall.Write(pair[1], []byte("GET / HTTP/1.1\r\nHost: test\r\n\r\n")); e != nil {
		t.Fatal(e)
	}
	if e = syscall.Shutdown(pair[1], syscall.SHUT_WR); e != nil {
		t.Fatal(e)
	}
	body := bytes.Repeat([]byte{'b'}, 1<<20)
	if e = s.connectionReady(c, syscall.EPOLLIN|syscall.EPOLLRDHUP, func(*Request) Response { return Response{Status: 200, Body: body} }); e != nil {
		t.Fatal(e)
	}
	if c.sent == 0 || c.sent >= len(c.out) {
		t.Fatalf("test did not produce a partial write: %d/%d", c.sent, len(c.out))
	}
	events := make([]syscall.EpollEvent, 8)
	n, e := syscall.EpollWait(s.epfd, events, 0)
	if e != nil {
		t.Fatal(e)
	}
	if n != 0 {
		t.Fatalf("backpressured half-close would spin: %+v", events[:n])
	}
}

func TestOwnerHalfClosedPipelineAndLargeResponse(t *testing.T) {
	body := bytes.Repeat([]byte("pipeline"), 1<<20)
	var called atomic.Int64
	port, stop, e := ServeOwner(func(*Request) Response { called.Add(1); return Response{Status: 200, Body: body} })
	if e != nil {
		t.Fatal(e)
	}
	defer stop()
	c := ownerClient(t, port)
	c.SetReadBuffer(65536)
	if _, e = io.WriteString(c, "GET /one HTTP/1.1\r\nHost: test\r\n\r\nGET /two HTTP/1.1\r\nHost: test\r\n\r\n"); e != nil {
		t.Fatal(e)
	}
	if e = c.CloseWrite(); e != nil {
		t.Fatal(e)
	}
	time.Sleep(30 * time.Millisecond)
	reader := bufio.NewReader(c)
	for range 2 {
		b, e := ownerReply(reader)
		if e != nil || !bytes.Equal(b, body) {
			t.Fatalf("half-close pipeline: %d bytes: %v", len(b), e)
		}
	}
	if _, e = reader.ReadByte(); e != io.EOF {
		t.Fatalf("expected EOF after drained pipeline: %v", e)
	}
	if called.Load() != 2 {
		t.Fatalf("dispatch count %d", called.Load())
	}
}

func TestOwnerHalfCloseDrainsMultipleInputBuffers(t *testing.T) {
	port, stop, e := ServeOwner(func(r *Request) Response { return Response{Status: 200, Body: []byte(r.URI)} })
	if e != nil {
		t.Fatal(e)
	}
	defer stop()
	c := ownerClient(t, port)
	var request bytes.Buffer
	const count = 400
	for i := range count {
		fmt.Fprintf(&request, "GET /%d HTTP/1.1\r\nHost: test\r\n\r\n", i)
	}
	if request.Len() <= 8192 {
		t.Fatal("test needs multiple input buffers")
	}
	if _, e = c.Write(request.Bytes()); e != nil {
		t.Fatal(e)
	}
	if e = c.CloseWrite(); e != nil {
		t.Fatal(e)
	}
	reader := bufio.NewReader(c)
	for i := range count {
		b, e := ownerReply(reader)
		if e != nil || string(b) != "/"+strconv.Itoa(i) {
			t.Fatalf("reply %d = %q: %v", i, b, e)
		}
	}
	if _, e = reader.ReadByte(); e != io.EOF {
		t.Fatalf("expected EOF: %v", e)
	}
}

func TestOwnerMaximumBodyContinueAndEOF(t *testing.T) {
	body := make([]byte, MaxBody)
	for i := range body {
		body[i] = byte(i * 31 % 251)
	}
	want := fmt.Sprintf("%x", sha256.Sum256(body))
	seen := make(chan int, 1)
	port, stop, e := ServeOwner(func(r *Request) Response {
		seen <- len(r.Body)
		return Response{Status: 200, Body: []byte(fmt.Sprintf("%x", sha256.Sum256(r.Body)))}
	})
	if e != nil {
		t.Fatal(e)
	}
	defer stop()
	c := ownerClient(t, port)
	reader := bufio.NewReader(c)
	if _, e = fmt.Fprintf(c, "POST /push HTTP/1.1\r\nHost: test\r\nContent-Length: %d\r\nExpect: 100-continue\r\n\r\n", len(body)); e != nil {
		t.Fatal(e)
	}
	interim, e := http.ReadResponse(reader, nil)
	if e != nil {
		t.Fatal(e)
	}
	interim.Body.Close()
	if interim.StatusCode != 100 {
		t.Fatalf("continue status %d", interim.StatusCode)
	}
	for at := 0; at < len(body); at += 32771 {
		end := min(len(body), at+32771)
		if _, e = c.Write(body[at:end]); e != nil {
			t.Fatal(e)
		}
	}
	if e = c.CloseWrite(); e != nil {
		t.Fatal(e)
	}
	b, e := ownerReply(reader)
	if e != nil || string(b) != want {
		t.Fatalf("large body hash %q: %v", b, e)
	}
	if n := <-seen; n != MaxBody {
		t.Fatalf("body size %d", n)
	}
	if _, e = reader.ReadByte(); e != io.EOF {
		t.Fatalf("large body connection did not close: %v", e)
	}
}

func TestOwnerIncompleteBodyEOFDoesNotDispatch(t *testing.T) {
	var called atomic.Int64
	port, stop, e := ServeOwner(func(*Request) Response { called.Add(1); return Response{Status: 200} })
	if e != nil {
		t.Fatal(e)
	}
	defer stop()
	c := ownerClient(t, port)
	if _, e = io.WriteString(c, "POST / HTTP/1.1\r\nHost: test\r\nContent-Length: 100\r\n\r\nshort"); e != nil {
		t.Fatal(e)
	}
	c.CloseWrite()
	var b [1]byte
	if _, e = c.Read(b[:]); e != io.EOF {
		t.Fatalf("truncated request: %v", e)
	}
	if called.Load() != 0 {
		t.Fatal("dispatched incomplete request")
	}
}

func TestOwnerHundredConnectionBurst(t *testing.T) {
	var called atomic.Int64
	port, stop, e := ServeOwner(func(r *Request) Response { called.Add(1); return Response{Status: 200, Body: []byte(r.URI)} })
	if e != nil {
		t.Fatal(e)
	}
	defer stop()
	start := make(chan struct{})
	errs := make(chan error, 100)
	var ready sync.WaitGroup
	ready.Add(100)
	for i := range 100 {
		go func() {
			c, e := net.Dial("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
			ready.Done()
			if e != nil {
				errs <- e
				return
			}
			defer c.Close()
			c.SetDeadline(time.Now().Add(15 * time.Second))
			<-start
			reader := bufio.NewReader(c)
			for j := range 5 {
				uri := fmt.Sprintf("/%d/%d", i, j)
				if _, e = fmt.Fprintf(c, "GET %s HTTP/1.1\r\nHost: test\r\n\r\n", uri); e != nil {
					errs <- e
					return
				}
				b, e := ownerReply(reader)
				if e != nil || string(b) != uri {
					errs <- fmt.Errorf("burst %s = %q: %v", uri, b, e)
					return
				}
			}
			errs <- nil
		}()
	}
	ready.Wait()
	close(start)
	for range 100 {
		if e = <-errs; e != nil {
			t.Fatal(e)
		}
	}
	if called.Load() != 500 {
		t.Fatalf("burst dispatch count %d", called.Load())
	}
}

func TestOwnerStopClosesIdleAndBackpressuredSockets(t *testing.T) {
	for _, large := range []bool{false, true} {
		t.Run(strconv.FormatBool(large), func(t *testing.T) {
			body := []byte("ok")
			if large {
				body = bytes.Repeat([]byte{'x'}, 8<<20)
			}
			port, stop, e := ServeOwner(func(*Request) Response { return Response{Status: 200, Body: body} })
			if e != nil {
				t.Fatal(e)
			}
			defer stop()
			for i := range 20 {
				c := ownerClient(t, port)
				if large && i == 0 {
					c.SetReadBuffer(4096)
					io.WriteString(c, "GET / HTTP/1.1\r\nHost: test\r\n\r\n")
				}
			}
			time.Sleep(20 * time.Millisecond)
			done := make(chan struct{})
			go func() { stop(); close(done) }()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("stop hung")
			}
			stop()
		})
	}
}

func TestOwnerFailedWakeupBecomesEOF(t *testing.T) {
	s, e := newOwnerServer(linuxOwnerSetup)
	if e != nil {
		t.Fatal(e)
	}
	defer s.finish()
	if e = syscall.Shutdown(s.wakeWrite, syscall.SHUT_WR); e != nil {
		t.Fatal(e)
	}
	s.signalStop()
	if s.wakeWrite != -1 {
		t.Fatal("failed wakeup writer left open")
	}
	events := make([]syscall.EpollEvent, 4)
	n, e := syscall.EpollWait(s.epfd, events, 1000)
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, event := range events[:n] {
		if int(event.Fd) == s.wakeRead {
			found = true
		}
	}
	if !found {
		t.Fatal("failed wakeup did not make watched peer ready")
	}
}

func TestOwnerStartStopDoesNotLeakDescriptors(t *testing.T) {
	count := func() int {
		fds, e := os.ReadDir("/proc/self/fd")
		if e != nil {
			t.Fatal(e)
		}
		return len(fds)
	}
	// Initialize Go's client poller before taking the baseline.
	port, stop, e := ServeOwner(func(*Request) Response { return Response{Status: 200} })
	if e != nil {
		t.Fatal(e)
	}
	c, e := net.Dial("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if e != nil {
		t.Fatal(e)
	}
	c.Close()
	stop()
	before := count()
	for range 12 {
		port, stop, e := ServeOwner(func(*Request) Response { return Response{Status: 200} })
		if e != nil {
			t.Fatal(e)
		}
		var clients []net.Conn
		for range 12 {
			c, e := net.Dial("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
			if e != nil {
				stop()
				t.Fatal(e)
			}
			clients = append(clients, c)
		}
		stop()
		for _, c := range clients {
			c.Close()
		}
	}
	if after := count(); after > before {
		t.Fatalf("fd count grew: %d -> %d", before, after)
	}
}

func TestOwnerDescriptorPressurePreservesConnections(t *testing.T) {
	if os.Getenv("FORGE_TEST_FD_PRESSURE") != "1" {
		executable, e := os.Executable()
		if e != nil {
			t.Fatal(e)
		}
		cmd := exec.Command(executable, "-test.run=^TestOwnerDescriptorPressurePreservesConnections$", "-test.timeout=10s")
		cmd.Env = append(os.Environ(), "FORGE_TEST_FD_PRESSURE=1")
		if output, e := cmd.CombinedOutput(); e != nil {
			t.Fatalf("fd pressure child: %v\n%s", e, output)
		}
		return
	}
	var limit syscall.Rlimit
	if e := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &limit); e != nil {
		t.Fatal(e)
	}
	limit.Cur = min(limit.Cur, 96)
	if e := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &limit); e != nil {
		t.Fatal(e)
	}
	port, stop, e := ServeOwner(func(*Request) Response { return Response{Status: 200, Body: []byte("ok")} })
	if e != nil {
		t.Fatal(e)
	}
	defer stop()
	active := ownerClient(t, port)
	reader := bufio.NewReader(active)
	io.WriteString(active, "GET / HTTP/1.1\r\nHost: test\r\n\r\n")
	if _, e = ownerReply(reader); e != nil {
		t.Fatal(e)
	}
	pending, e := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, syscall.IPPROTO_TCP)
	if e != nil {
		t.Fatal(e)
	}
	defer func() {
		if pending >= 0 {
			syscall.Close(pending)
		}
	}()
	timeout := syscall.NsecToTimeval(int64(3 * time.Second))
	syscall.SetsockoptTimeval(pending, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &timeout)
	var padding []int
	for {
		fd, e := syscall.Open("/dev/null", syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
		if e == syscall.EMFILE {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		padding = append(padding, fd)
	}
	defer func() {
		for _, fd := range padding {
			syscall.Close(fd)
		}
	}()
	if len(padding) == 0 {
		t.Fatal("no fd padding")
	}
	if e = syscall.Connect(pending, &syscall.SockaddrInet4{Port: port, Addr: [4]byte{127, 0, 0, 1}}); e != nil {
		t.Fatal(e)
	}
	if _, e = syscall.Write(pending, []byte("GET / HTTP/1.1\r\nHost: test\r\nConnection: close\r\n\r\n")); e != nil {
		t.Fatal(e)
	}
	time.Sleep(40 * time.Millisecond)
	if _, e = io.WriteString(active, "GET / HTTP/1.1\r\nHost: test\r\n\r\n"); e != nil {
		t.Fatal(e)
	}
	if b, e := ownerReply(reader); e != nil || string(b) != "ok" {
		t.Fatalf("existing connection dropped under fd pressure: %q %v", b, e)
	}
	last := len(padding) - 1
	syscall.Close(padding[last])
	padding = padding[:last]
	readReply := func(fd int) ([]byte, error) {
		var out bytes.Buffer
		var chunk [1024]byte
		for {
			n, e := syscall.Read(fd, chunk[:])
			if n > 0 {
				out.Write(chunk[:n])
			}
			if e == syscall.EINTR {
				continue
			}
			if e != nil || n == 0 {
				return out.Bytes(), e
			}
		}
	}
	response, e := readReply(pending)
	if e == nil && bytes.Contains(response, []byte("200 OK")) && bytes.HasSuffix(response, []byte("ok")) {
		return
	}
	// gVisor can dequeue and destroy a peer before discovering that the fd
	// table is full. Such a peer is never returned by accept4; application
	// code cannot retain it. Verify recovery independently of that reset.
	if len(response) != 0 || (e != nil && e != syscall.ECONNRESET) {
		t.Fatalf("unexpected pressure reply %q: %v", response, e)
	}
	t.Log("kernel discarded the unaccepted connection at EMFILE; checking a fresh accept")
	syscall.Close(pending)
	pending = -1
	retry, e := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, syscall.IPPROTO_TCP)
	if e != nil {
		t.Fatal(e)
	}
	defer syscall.Close(retry)
	if e = syscall.SetsockoptTimeval(retry, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &timeout); e != nil {
		t.Fatal(e)
	}
	if e = syscall.Connect(retry, &syscall.SockaddrInet4{Port: port, Addr: [4]byte{127, 0, 0, 1}}); e != nil {
		t.Fatal(e)
	}
	if _, e = syscall.Write(retry, []byte("GET / HTTP/1.1\r\nHost: test\r\nConnection: close\r\n\r\n")); e != nil {
		t.Fatal(e)
	}
	response, e = readReply(retry)
	if e != nil || !bytes.Contains(response, []byte("200 OK")) || !bytes.HasSuffix(response, []byte("ok")) {
		t.Fatalf("fresh accept did not recover: %q %v", response, e)
	}
}
