//go:build linux
// +build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path"
	"sync"
	"sync/atomic"
	"syscall"

	"google.golang.org/grpc"
)

const (
	debugUnixScheme = "unix"
	debugTCPScheme  = "tcp"
)

var listenerConnSeq atomic.Uint64

func listenAndServeWithDebug(ctx context.Context, address *url.URL, server *grpc.Server) <-chan error {
	errCh := make(chan error, 1)

	network, target := listenURLToNetworkTarget(address)
	if network == debugUnixScheme {
		if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
			errCh <- fmt.Errorf("cannot delete existing socket file %s: %w", target, err)
			close(errCh)
			return errCh
		}

		basePath := path.Dir(target)
		if _, err := os.Stat(basePath); errors.Is(err, os.ErrNotExist) {
			transportDebugf("listener mkdir: path=%s", basePath)
			if err := os.MkdirAll(basePath, os.ModePerm); err != nil {
				errCh <- fmt.Errorf("create socket dir %s: %w", basePath, err)
				close(errCh)
				return errCh
			}
		}
	}

	ln, err := net.Listen(network, target)
	if err != nil {
		errCh <- err
		close(errCh)
		return errCh
	}

	if network == debugUnixScheme {
		if chmodErr := os.Chmod(target, os.ModePerm); chmodErr != nil {
			errCh <- fmt.Errorf("chmod socket %s: %w", target, chmodErr)
			close(errCh)
			_ = ln.Close()
			return errCh
		}
	}

	debugLn := &loggingListener{
		Listener: ln,
		network:  network,
		target:   target,
	}
	transportDebugf(
		"listener ready: network=%s target=%s addr=%s listener_type=%T",
		network,
		target,
		ln.Addr(),
		ln,
	)

	go func() {
		defer func() {
			transportDebugf("listener closing: network=%s target=%s", network, target)
			_ = debugLn.Close()
		}()

		go func() {
			<-ctx.Done()
			transportDebugf("listener context canceled: network=%s target=%s", network, target)
			server.Stop()
		}()

		if serveErr := server.Serve(debugLn); serveErr != nil {
			transportDebugf("listener serve returned error: network=%s target=%s err=%v", network, target, serveErr)
			errCh <- serveErr
		}
		close(errCh)
	}()

	return errCh
}

func listenURLToNetworkTarget(u *url.URL) (network, target string) {
	network = debugTCPScheme
	target = u.Host
	if u.Scheme == debugUnixScheme {
		network = debugUnixScheme
		target = u.Path
		if target == "" {
			target = u.Opaque
		}
	}
	return network, target
}

type loggingListener struct {
	net.Listener
	network string
	target  string
}

func (l *loggingListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		transportDebugf("listener accept error: network=%s target=%s err=%v", l.network, l.target, err)
		return nil, err
	}

	id := listenerConnSeq.Add(1)
	transportDebugf(
		"listener accept: conn_id=%d network=%s target=%s remote=%v local=%v conn_type=%T",
		id,
		l.network,
		l.target,
		conn.RemoteAddr(),
		conn.LocalAddr(),
		conn,
	)

	return &loggingConn{
		Conn: conn,
		id:   id,
	}, nil
}

type loggingConn struct {
	net.Conn
	id        uint64
	readOnce  sync.Once
	writeOnce sync.Once
	closeOnce sync.Once
}

// Preserve Unix socket capabilities across the debug wrapper so grpcfd can
// continue sending and receiving file descriptors on wrapped listener conns.
func (c *loggingConn) ReadMsgUnix(b []byte, oob []byte) (int, int, int, *net.UnixAddr, error) {
	conn, ok := c.Conn.(interface {
		ReadMsgUnix(b []byte, oob []byte) (n, oobn, flags int, addr *net.UnixAddr, err error)
	})
	if !ok {
		return 0, 0, 0, nil, fmt.Errorf("underlying conn %T does not support ReadMsgUnix", c.Conn)
	}
	return conn.ReadMsgUnix(b, oob)
}

func (c *loggingConn) WriteMsgUnix(b []byte, oob []byte, addr *net.UnixAddr) (int, int, error) {
	conn, ok := c.Conn.(interface {
		WriteMsgUnix(b []byte, oob []byte, addr *net.UnixAddr) (n, oobn int, err error)
	})
	if !ok {
		return 0, 0, fmt.Errorf("underlying conn %T does not support WriteMsgUnix", c.Conn)
	}
	return conn.WriteMsgUnix(b, oob, addr)
}

func (c *loggingConn) SyscallConn() (syscall.RawConn, error) {
	conn, ok := c.Conn.(interface {
		SyscallConn() (syscall.RawConn, error)
	})
	if !ok {
		return nil, fmt.Errorf("underlying conn %T does not support SyscallConn", c.Conn)
	}
	return conn.SyscallConn()
}

func (c *loggingConn) File() (*os.File, error) {
	conn, ok := c.Conn.(interface {
		File() (*os.File, error)
	})
	if !ok {
		return nil, fmt.Errorf("underlying conn %T does not support File", c.Conn)
	}
	return conn.File()
}

func (c *loggingConn) CloseRead() error {
	if conn, ok := c.Conn.(interface{ CloseRead() error }); ok {
		return conn.CloseRead()
	}
	return nil
}

func (c *loggingConn) CloseWrite() error {
	if conn, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return conn.CloseWrite()
	}
	return nil
}

func (c *loggingConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.readOnce.Do(func() {
		transportDebugf(
			"listener conn first read: conn_id=%d remote=%v local=%v bytes=%d err=%v",
			c.id,
			c.Conn.RemoteAddr(),
			c.Conn.LocalAddr(),
			n,
			err,
		)
	})
	return n, err
}

func (c *loggingConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.writeOnce.Do(func() {
		transportDebugf(
			"listener conn first write: conn_id=%d remote=%v local=%v bytes=%d err=%v",
			c.id,
			c.Conn.RemoteAddr(),
			c.Conn.LocalAddr(),
			n,
			err,
		)
	})
	return n, err
}

func (c *loggingConn) Close() error {
	err := c.Conn.Close()
	c.closeOnce.Do(func() {
		if err != nil && !errors.Is(err, io.EOF) {
			transportDebugf(
				"listener conn close error: conn_id=%d remote=%v local=%v err=%v",
				c.id,
				c.Conn.RemoteAddr(),
				c.Conn.LocalAddr(),
				err,
			)
			return
		}
		transportDebugf(
			"listener conn closed: conn_id=%d remote=%v local=%v err=%v",
			c.id,
			c.Conn.RemoteAddr(),
			c.Conn.LocalAddr(),
			err,
		)
	})
	return err
}
