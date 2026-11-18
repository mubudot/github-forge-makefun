package transport

import (
	"io"
	"runtime"
	"sync"
	"syscall"
	"time"
)

// Setup calls are passed per instance so failure cleanup can be checked without
// replacing process-global syscall functions or changing the request path.
type ownerSetupCalls struct {
	socket  func(int, int, int) (int, error)
	bind    func(int, syscall.Sockaddr) error
	listen  func(int, int) error
	address func(int) (syscall.Sockaddr, error)
	epoll   func(int) (int, error)
	pair    func(int, int, int) ([2]int, error)
	control func(int, int, int, uint32, int32) error
	close   func(int) error
}

var linuxOwnerSetup = ownerSetupCalls{
	socket: syscall.Socket, bind: syscall.Bind, listen: syscall.Listen,
	address: syscall.Getsockname, epoll: syscall.EpollCreate1,
	pair: syscall.Socketpair, control: ownerControl, close: syscall.Close,
}

type ownerServer struct {
	listener, epfd, wakeRead, wakeWrite int
	port                                int
	closeFD                             func(int) error
	stopOnce                            sync.Once
	wakeMu                              sync.Mutex
	done                                chan struct{}
	connections                         map[int]*ownerConn
	serial                              int32
	acceptPaused                        bool
	acceptRetry                         time.Time
}

// ServeOwner uses one locked thread and level-triggered epoll. All socket I/O
// is nonblocking. A preemption-interrupted raw wait returns through a normal Go
// syscall before waiting again, allowing GC/STW without changing signal handlers.
// A failed setup is completely closed before the existing transport is started.
func ServeOwner(h Handler) (int, func(), error) {
	return serveOwnerWithFallback(h, linuxOwnerSetup, ServeBlocking)
}

func serveOwnerWithFallback(h Handler, calls ownerSetupCalls, fallback func(Handler) (int, func(), error)) (int, func(), error) {
	s, e := newOwnerServer(calls)
	if e != nil {
		return fallback(h)
	}
	go s.run(h)
	return s.port, s.stop, nil
}

func newOwnerServer(calls ownerSetupCalls) (s *ownerServer, err error) {
	s = &ownerServer{listener: -1, epfd: -1, wakeRead: -1, wakeWrite: -1,
		closeFD: calls.close, done: make(chan struct{}), connections: make(map[int]*ownerConn)}
	defer func(server *ownerServer) {
		if err != nil {
			server.closeDescriptors()
		}
	}(s)
	s.listener, err = calls.socket(syscall.AF_INET, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC|syscall.SOCK_NONBLOCK, syscall.IPPROTO_TCP)
	if err != nil {
		return nil, err
	}
	if err = calls.bind(s.listener, &syscall.SockaddrInet4{}); err != nil {
		return nil, err
	}
	if err = calls.listen(s.listener, 128); err != nil {
		return nil, err
	}
	addr, e := calls.address(s.listener)
	if e != nil {
		return nil, e
	}
	inet, ok := addr.(*syscall.SockaddrInet4)
	if !ok {
		return nil, syscall.EAFNOSUPPORT
	}
	s.port = inet.Port
	s.epfd, err = calls.epoll(syscall.EPOLL_CLOEXEC)
	if err != nil {
		return nil, err
	}
	pair, e := calls.pair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC|syscall.SOCK_NONBLOCK, 0)
	if e != nil {
		return nil, e
	}
	s.wakeRead, s.wakeWrite = pair[0], pair[1]
	if err = calls.control(s.epfd, syscall.EPOLL_CTL_ADD, s.listener, syscall.EPOLLIN, 0); err != nil {
		return nil, err
	}
	if err = calls.control(s.epfd, syscall.EPOLL_CTL_ADD, s.wakeRead, syscall.EPOLLIN, 0); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *ownerServer) closeDescriptors() {
	for _, fd := range []*int{&s.listener, &s.epfd, &s.wakeRead} {
		if *fd >= 0 {
			s.closeFD(*fd)
			*fd = -1
		}
	}
	s.wakeMu.Lock()
	if s.wakeWrite >= 0 {
		s.closeFD(s.wakeWrite)
		s.wakeWrite = -1
	}
	s.wakeMu.Unlock()
}

func (s *ownerServer) signalStop() {
	s.wakeMu.Lock()
	defer s.wakeMu.Unlock()
	if s.wakeWrite < 0 {
		return
	}
	n, e := rawSendFD(s.wakeWrite, []byte{1})
	// EAGAIN means a previous byte is already queued. On any other failed
	// wakeup, closing this end makes the watched peer readable at EOF/HUP.
	if (e != 0 && e != syscall.EAGAIN) || (e == 0 && n != 1) {
		s.closeFD(s.wakeWrite)
		s.wakeWrite = -1
	}
}
func (s *ownerServer) stop() {
	s.stopOnce.Do(func() { s.signalStop(); <-s.done })
}
func (s *ownerServer) finish() {
	for fd := range s.connections {
		s.closeFD(fd)
	}
	clear(s.connections)
	s.closeDescriptors()
	close(s.done)
}
func (s *ownerServer) closeConnection(c *ownerConn) {
	delete(s.connections, c.fd)
	s.closeFD(c.fd)
	if s.acceptPaused {
		s.acceptRetry = time.Time{}
	}
}

// A readable listener at the fd/memory limit would otherwise wake epoll in a
// tight loop. Keep established connections alive and retry only after a close
// or a bounded backoff. Ordinary requests do not read a clock or set a timer.
func (s *ownerServer) pauseAccept() error {
	if err := ownerControl(s.epfd, syscall.EPOLL_CTL_DEL, s.listener, 0, 0); err != nil {
		return err
	}
	s.acceptPaused = true
	s.acceptRetry = time.Now().Add(100 * time.Millisecond)
	return nil
}
func (s *ownerServer) waitTimeout() int {
	if !s.acceptPaused {
		return -1
	}
	remaining := time.Until(s.acceptRetry)
	if remaining <= 0 {
		if err := ownerControl(s.epfd, syscall.EPOLL_CTL_ADD, s.listener, syscall.EPOLLIN, 0); err == nil {
			s.acceptPaused = false
			return -1
		}
		remaining = 100 * time.Millisecond
		s.acceptRetry = time.Now().Add(remaining)
	}
	return int((remaining + time.Millisecond - 1) / time.Millisecond)
}

func (s *ownerServer) acceptReady() error {
	for accepted := 0; accepted < 128; accepted++ {
		client, e := rawAcceptFD(s.listener)
		switch e {
		case syscall.EINTR, syscall.ECONNABORTED, syscall.ENETDOWN, syscall.EPROTO,
			syscall.ENOPROTOOPT, syscall.EHOSTDOWN, syscall.ENONET,
			syscall.EHOSTUNREACH, syscall.EOPNOTSUPP, syscall.ENETUNREACH:
			continue
		case syscall.EAGAIN:
			return nil
		case syscall.EMFILE, syscall.ENFILE, syscall.ENOBUFS, syscall.ENOMEM:
			return s.pauseAccept()
		case 0:
		default:
			return e
		}
		rawNoDelay(client)
		s.serial++
		if s.serial == 0 {
			s.serial++
		}
		c := &ownerConn{fd: client, serial: s.serial, input: make([]byte, 8192),
			out: make([]byte, 0, 4096), interest: syscall.EPOLLIN | syscall.EPOLLRDHUP}
		if err := ownerControl(s.epfd, syscall.EPOLL_CTL_ADD, client, c.interest, c.serial); err != nil {
			s.closeFD(client)
			continue
		}
		s.connections[client] = c
	}
	return nil
}

func (s *ownerServer) connectionReady(c *ownerConn, events uint32, h Handler) error {
	if events&syscall.EPOLLERR != 0 {
		return io.ErrClosedPipe
	}
	if len(c.out) > 0 {
		if err := c.flush(); err != nil {
			return err
		}
		if len(c.out) == 0 {
			if err := c.dispatch(h); err != nil {
				return err
			}
		}
	}
	if len(c.out) == 0 && events&(syscall.EPOLLIN|syscall.EPOLLRDHUP|syscall.EPOLLHUP) != 0 {
		if err := c.receive(h); err != nil {
			if err == syscall.EAGAIN {
				if events&(syscall.EPOLLRDHUP|syscall.EPOLLHUP) != 0 {
					return io.EOF
				}
				return nil
			}
			return err
		}
	}
	// HUP is reported even when not requested. A fully disconnected peer with
	// unwritable output cannot recover; retaining it would spin. A positive
	// read with drained output must instead continue until the actual EOF,
	// because HUP may arrive with more than one buffer of unread requests.
	if events&syscall.EPOLLHUP != 0 && len(c.out) > 0 {
		return io.ErrClosedPipe
	}
	interest := uint32(syscall.EPOLLIN | syscall.EPOLLRDHUP)
	if len(c.out) > 0 {
		// RDHUP is level-triggered too. While backpressured, subscribe only to
		// writability so a client that CloseWrite()s cannot cause a busy loop.
		interest = syscall.EPOLLOUT
	}
	if interest != c.interest {
		if err := ownerControl(s.epfd, syscall.EPOLL_CTL_MOD, c.fd, interest, c.serial); err != nil {
			return err
		}
		c.interest = interest
	}
	return nil
}

func (s *ownerServer) run(h Handler) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer s.finish()
	events := make([]syscall.EpollEvent, 128)
	rawAllowed := rawWaitEnabled()
	rawNext := rawAllowed
	for {
		n, e := ownerWait(s.epfd, events, s.waitTimeout(), rawNext)
		if e == syscall.EINTR {
			rawNext = false
			continue
		}
		if e != nil {
			return
		}
		rawNext = rawAllowed
		for _, event := range events[:n] {
			fd := int(event.Fd)
			if fd == s.wakeRead {
				return
			}
			if fd == s.listener {
				if !s.acceptPaused {
					if e = s.acceptReady(); e != nil {
						return
					}
				}
				continue
			}
			c := s.connections[fd]
			if c == nil || c.serial != event.Pad {
				continue
			}
			if e = s.connectionReady(c, event.Events, h); e != nil {
				s.closeConnection(c)
			}
		}
	}
}
