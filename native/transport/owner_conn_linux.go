package transport

import (
	"bytes"
	"io"
	"strings"
	"syscall"
)

// ownerConn is touched only by the event-loop goroutine. Output backpressure
// pauses input, so one response plus a bounded pipeline is queued per socket.
type ownerConn struct {
	fd                 int
	serial             int32
	input, out         []byte
	used, start, sent  int
	parser             parser
	continued, closing bool
	interest           uint32
}

func (c *ownerConn) flush() error {
	for c.sent < len(c.out) {
		n, e := rawSendFD(c.fd, c.out[c.sent:])
		if e == syscall.EAGAIN {
			return nil
		}
		if e != 0 {
			return e
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		c.sent += n
	}
	c.out = c.out[:0]
	c.sent = 0
	if c.closing {
		return io.EOF
	}
	return nil
}
func (c *ownerConn) dispatch(h Handler) error {
	for {
		r, n, e := c.parser.parse(c.input[c.start:c.used])
		if e != nil {
			c.out = AppendResponse(c.out, JSON(400, map[string]any{"message": "Invalid HTTP request"}), true)
			c.closing = true
			return c.flush()
		}
		if n == 0 {
			break
		}
		c.out = AppendResponse(c.out, h(&r), r.Close)
		c.start += n
		c.continued = false
		if r.Close {
			c.closing = true
			return c.flush()
		}
		if len(c.out) >= 256<<10 {
			if e = c.flush(); e != nil {
				return e
			}
			if len(c.out) > 0 {
				return nil
			}
		}
	}
	if len(c.out) > 0 {
		if e := c.flush(); e != nil {
			return e
		}
		if len(c.out) > 0 {
			return nil
		}
	}
	if !c.continued && bytes.Contains(c.input[c.start:c.used], []byte("\r\n\r\n")) && strings.EqualFold(c.parser.template.Headers["expect"], "100-continue") {
		c.out = append(c.out, "HTTP/1.1 100 Continue\r\n\r\n"...)
		c.continued = true
		return c.flush()
	}
	return nil
}
func (c *ownerConn) receive(h Handler) error {
	if c.start > 0 {
		copy(c.input, c.input[c.start:c.used])
		c.used -= c.start
		c.start = 0
	}
	if c.used == len(c.input) {
		if c.used >= MaxBody+maxHeader {
			c.out = AppendResponse(c.out, JSON(413, map[string]any{"message": "Request body too large"}), true)
			c.closing = true
			return c.flush()
		}
		c.input = append(c.input, make([]byte, min(len(c.input), MaxBody+maxHeader-c.used))...)
	}
	n, e := rawReadFD(c.fd, c.input[c.used:])
	if e == syscall.EAGAIN {
		return e
	}
	if e != 0 {
		return e
	}
	if n == 0 {
		return io.EOF
	}
	c.used += n
	return c.dispatch(h)
}
