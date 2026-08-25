package rendradapter

import (
	"context"
	"errors"
	"net"
	"sync"
)

var errCarrierListenerClosed = errors.New("rendradapter: carrier listener closed")

type carrierListener struct {
	queue  chan net.Conn
	closed chan struct{}
	once   sync.Once
}

func newCarrierListener() *carrierListener {
	return &carrierListener{
		queue:  make(chan net.Conn),
		closed: make(chan struct{}),
	}
}

func (l *carrierListener) Inject(ctx context.Context, conn net.Conn) error {
	if ctx == nil {
		return errors.New("rendradapter: nil carrier context")
	}
	if conn == nil {
		return errors.New("rendradapter: nil carrier connection")
	}
	select {
	case l.queue <- conn:
		return nil
	case <-l.closed:
		return errCarrierListenerClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l *carrierListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.queue:
		return conn, nil
	case <-l.closed:
		return nil, errCarrierListenerClosed
	}
}

func (l *carrierListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *carrierListener) Addr() net.Addr { return carrierAddr("xray-stream") }

type carrierAddr string

func (a carrierAddr) Network() string { return "xray" }
func (a carrierAddr) String() string  { return string(a) }
