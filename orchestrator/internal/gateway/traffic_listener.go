package gateway

import (
	"net"
	"sync"
)

// LimitTCPConnections bounds active and idle traffic connections. Control uses
// its own Unix listener and remains available when guest traffic is saturated.
func LimitTCPConnections(listener net.Listener, maximum int) net.Listener {
	if maximum < 1 {
		panic("positive TCP connection limit required")
	}
	return &limitedTCPListener{Listener: listener, slots: make(chan struct{}, maximum), done: make(chan struct{})}
}

type limitedTCPListener struct {
	net.Listener
	slots chan struct{}
	done  chan struct{}
	once  sync.Once
	err   error
}

func (l *limitedTCPListener) Accept() (net.Conn, error) {
	select {
	case l.slots <- struct{}{}:
	case <-l.done:
		return nil, net.ErrClosed
	}
	connection, err := l.Listener.Accept()
	if err != nil {
		<-l.slots
		return nil, err
	}
	tcp, ok := connection.(*net.TCPConn)
	if !ok {
		_ = connection.Close()
		<-l.slots
		return nil, &net.OpError{Op: "accept", Net: "tcp", Err: net.ErrClosed}
	}
	return &limitedTCPConn{TCPConn: tcp, release: func() { <-l.slots }}, nil
}
func (l *limitedTCPListener) Close() error {
	l.once.Do(func() { close(l.done); l.err = l.Listener.Close() })
	return l.err
}

type limitedTCPConn struct {
	*net.TCPConn
	release func()
	once    sync.Once
}

func (c *limitedTCPConn) Close() error { err := c.TCPConn.Close(); c.once.Do(c.release); return err }
