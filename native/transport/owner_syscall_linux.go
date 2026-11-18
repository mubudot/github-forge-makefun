package transport

import (
	"os"
	"runtime"
	"strings"
	"syscall"
	"unsafe"
)

func rawReadFD(fd int, b []byte) (int, syscall.Errno) {
	for {
		n, _, e := syscall.RawSyscall(syscall.SYS_READ, uintptr(fd), uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)))
		runtime.KeepAlive(b)
		if e == syscall.EINTR {
			continue
		}
		return int(n), e
	}
}
func rawSendFD(fd int, b []byte) (int, syscall.Errno) {
	for {
		n, _, e := syscall.RawSyscall6(syscall.SYS_SENDTO, uintptr(fd), uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)), syscall.MSG_DONTWAIT|syscall.MSG_NOSIGNAL, 0, 0)
		runtime.KeepAlive(b)
		if e == syscall.EINTR {
			continue
		}
		return int(n), e
	}
}
func ownerControl(epfd, op, fd int, flags uint32, serial int32) error {
	event := syscall.EpollEvent{Events: flags, Fd: int32(fd), Pad: serial}
	for {
		_, _, e := syscall.RawSyscall6(syscall.SYS_EPOLL_CTL, uintptr(epfd), uintptr(op), uintptr(fd), uintptr(unsafe.Pointer(&event)), 0, 0)
		runtime.KeepAlive(&event)
		if e == syscall.EINTR {
			continue
		}
		if e != 0 {
			return e
		}
		return nil
	}
}
func ownerWait(epfd int, events []syscall.EpollEvent, timeout int, raw bool) (int, error) {
	if raw {
		n, _, e := syscall.RawSyscall6(syscall.SYS_EPOLL_PWAIT, uintptr(epfd), uintptr(unsafe.Pointer(&events[0])), uintptr(len(events)), uintptr(timeout), 0, 8)
		runtime.KeepAlive(events)
		if e != 0 {
			return 0, e
		}
		return int(n), nil
	}
	return syscall.EpollWait(epfd, events, timeout)
}
func rawWaitEnabled() bool {
	if runtime.GOMAXPROCS(0) < 2 || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		return false
	}
	for _, field := range strings.Split(os.Getenv("GODEBUG"), ",") {
		if k, v, ok := strings.Cut(field, "="); ok && k == "asyncpreemptoff" && v != "0" {
			return false
		}
	}
	return true
}

func rawAcceptFD(fd int) (int, syscall.Errno) {
	n, _, e := syscall.RawSyscall6(syscall.SYS_ACCEPT4, uintptr(fd), 0, 0, syscall.SOCK_CLOEXEC|syscall.SOCK_NONBLOCK, 0, 0)
	return int(n), e
}
func rawNoDelay(fd int) {
	var one int32 = 1
	syscall.RawSyscall6(syscall.SYS_SETSOCKOPT, uintptr(fd), syscall.IPPROTO_TCP, syscall.TCP_NODELAY, uintptr(unsafe.Pointer(&one)), 4, 0)
	runtime.KeepAlive(&one)
}
