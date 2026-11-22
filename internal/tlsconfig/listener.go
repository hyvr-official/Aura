package tlsconfig

import (
	"bufio"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"
)

const tlsHandshakeByte = 0x16

type sniffedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *sniffedConn) Read(p []byte) (int, error) {
	return c.reader.Read(p)
}

type chanListener struct {
	addr   net.Addr
	conns  chan net.Conn
	done   chan struct{}
	closer sync.Once
}

func newChanListener(addr net.Addr) *chanListener {
	return &chanListener{addr: addr, conns: make(chan net.Conn), done: make(chan struct{})}
}

func (l *chanListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.conns:
		return conn, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *chanListener) Close() error {
	l.closer.Do(func() { close(l.done) })
	return nil
}

func (l *chanListener) Addr() net.Addr {
	return l.addr
}

func (l *chanListener) push(conn net.Conn) {
	select {
	case l.conns <- conn:
	case <-l.done:
		conn.Close()
	}
}

func Serve(addr string, handler http.Handler, cfg *tls.Config) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	defer ln.Close()

	secure := newChanListener(ln.Addr())
	plain := newChanListener(ln.Addr())
	defer secure.Close()
	defer plain.Close()

	errs := make(chan error, 3)

	go func() {
		server := &http.Server{Handler: handler}
		errs <- server.Serve(tls.NewListener(secure, cfg))
	}()
	go func() {
		server := &http.Server{Handler: http.HandlerFunc(redirect), ReadHeaderTimeout: 10 * time.Second}
		errs <- server.Serve(plain)
	}()
	go func() {
		errs <- dispatch(ln, secure, plain)
	}()

	return <-errs
}

func dispatch(ln net.Listener, secure, plain *chanListener) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				continue
			}
			return err
		}
		go route(conn, secure, plain)
	}
}

func route(conn net.Conn, secure, plain *chanListener) {
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	reader := bufio.NewReader(conn)
	first, err := reader.Peek(1)
	if err != nil {
		conn.Close()
		return
	}
	conn.SetReadDeadline(time.Time{})
	wrapped := &sniffedConn{Conn: conn, reader: reader}
	if first[0] == tlsHandshakeByte {
		secure.push(wrapped)
		return
	}
	plain.push(wrapped)
}

func redirect(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "https://"+r.Host+r.URL.RequestURI(), http.StatusPermanentRedirect)
}
