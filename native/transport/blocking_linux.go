package transport

import (
	"io"
	"net"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"
)

// Blocking socket calls release the Go P but skip the readiness probe, EAGAIN
// retry and epoll cycle on every request. A locked thread owns each descriptor.
// Extra connections use the netpoller to keep the process below its pid budget.
type socket struct {
	fd        int
	once      sync.Once
	lastWrite uint64
	shortGaps uint8
}

func (s *socket) Read(b []byte) (int, error) {
	for {
		n, e := syscall.Read(s.fd, b)
		if e == syscall.EINTR {
			continue
		}
		if n == 0 && e == nil {
			return 0, io.EOF
		}
		return n, e
	}
}

var writeSequence atomic.Uint64

func (s *socket) Write(b []byte) (int, error) {
	sequence := writeSequence.Add(1)
	if s.lastWrite != 0 && sequence-s.lastWrite <= 4 {
		if s.shortGaps < 4 {
			s.shortGaps++
		}
	} else {
		s.shortGaps = 0
	}
	s.lastWrite = sequence
	// Recently alternating connections can send without relinquishing a Go P.
	// A concurrent fan-out has longer gaps and keeps the normal blocking path.
	// MSG_DONTWAIT bounds the fast syscall; backpressure uses the blocking write.
	if s.shortGaps >= 4 && len(b) > 0 && len(b) <= 4096 {
		n, _, errno := syscall.RawSyscall6(syscall.SYS_SENDTO, uintptr(s.fd), uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)), syscall.MSG_DONTWAIT|syscall.MSG_NOSIGNAL, 0, 0)
		runtime.KeepAlive(b)
		if errno == 0 {
			return int(n), nil
		}
		if errno != syscall.EAGAIN && errno != syscall.EINTR {
			return int(n), errno
		}
	}

	for {
		n, e := syscall.Write(s.fd, b)
		if e == syscall.EINTR {
			continue
		}
		return n, e
	}
}
func (s *socket) Close() error { var e error; s.once.Do(func() { e = syscall.Close(s.fd) }); return e }
func ServeBlocking(h Handler) (port int, closeServer func(), err error) {
	return serveBlocking(h, 112)
}

func serveBlocking(h Handler, connectionLimit int) (port int, closeServer func(), err error) {
	fd, e := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, syscall.IPPROTO_TCP)
	if e != nil {
		return 0, nil, e
	}
	success := false
	defer func() {
		if !success {
			syscall.Close(fd)
		}
	}()
	if e = syscall.Bind(fd, &syscall.SockaddrInet4{}); e != nil {
		return 0, nil, e
	}
	if e = syscall.Listen(fd, 128); e != nil {
		return 0, nil, e
	}
	addr, e := syscall.Getsockname(fd)
	if e != nil {
		return 0, nil, e
	}
	port = addr.(*syscall.SockaddrInet4).Port
	slots := make(chan struct{}, connectionLimit)
	var mu sync.Mutex
	active := map[*socket]bool{}
	stopped := false
	var once sync.Once
	closeServer = func() {
		once.Do(func() {
			mu.Lock()
			stopped = true
			syscall.Shutdown(fd, syscall.SHUT_RDWR)
			syscall.Close(fd)
			for s := range active {
				syscall.Shutdown(s.fd, syscall.SHUT_RDWR)
			}
			mu.Unlock()
		})
	}
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		for {
			client, _, e := syscall.Accept4(fd, syscall.SOCK_CLOEXEC)
			if e == syscall.EINTR {
				continue
			}
			if e != nil {
				return
			}
			syscall.SetsockoptInt(client, syscall.IPPROTO_TCP, syscall.TCP_NODELAY, 1)
			s := &socket{fd: client}
			mu.Lock()
			if stopped {
				mu.Unlock()
				s.Close()
				return
			}
			active[s] = true
			mu.Unlock()
			go func() {
				defer func() { mu.Lock(); delete(active, s); mu.Unlock() }()
				select {
				case slots <- struct{}{}:
					runtime.LockOSThread()
					defer runtime.UnlockOSThread()
					defer func() { <-slots }()
					serve(s, h)
				default: // A bounded thread count also serves arbitrary client concurrency.
					f := os.NewFile(uintptr(client), "forge-socket")
					c, e := net.FileConn(f)
					if e == nil {
						serve(c, h)
					}
					f.Close()
				}
			}()
		}
	}()
	success = true
	return port, closeServer, nil
}
