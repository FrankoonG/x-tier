// Package rendradapter is the only production package that imports rendr.
package rendradapter

import (
	"context"
	"errors"
	"net"
	"sync"

	"github.com/FrankoonG/rendr"
	"github.com/FrankoonG/x-tier/internal/route"
)

const chainTransport = "xtier-chain"

// StreamListener is retained only for the private legacy topology regression.
type StreamListener interface {
	Accept(context.Context) (net.Conn, error)
	Close() error
	Addr() net.Addr
}

type LegacyDialer struct {
	runtime *rendr.Runtime
	root    rendr.Target
	err     error
}

func NewLegacyDialer(compiled route.CompiledRoute) *LegacyDialer {
	runtime, err := rendr.NewRuntime(rendr.DefaultRuntimeConfig())
	return &LegacyDialer{runtime: runtime, root: buildTarget(compiled), err: err}
}

func (d *LegacyDialer) AddStreamPathFactory(name string, factory func(context.Context, string) (net.Conn, error)) error {
	if d == nil {
		return errors.New("rendradapter: nil legacy dialer")
	}
	if d.err != nil {
		return d.err
	}
	return d.runtime.RegisterStreamFactory(name, rendr.StreamFactory{
		Carrier: rendr.CarrierTCP,
		Dial:    factory,
	})
}

func (d *LegacyDialer) Dial(ctx context.Context) (net.Conn, error) {
	if d == nil {
		return nil, errors.New("rendradapter: nil legacy dialer")
	}
	if d.err != nil {
		return nil, d.err
	}
	return d.runtime.Dial(ctx, rendr.SessionConfig{Root: d.root})
}

func ListenLegacyTCP(addr string) (StreamListener, error) {
	runtime, err := rendr.NewRuntime(rendr.DefaultRuntimeConfig())
	if err != nil {
		return nil, err
	}
	raw, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	listener, err := runtime.Listen(rendr.ListenConfig{Streams: []rendr.StreamSource{{
		Name:     "legacy-tcp",
		Carrier:  rendr.CarrierTCP,
		Listener: raw,
	}}})
	if err != nil {
		_ = raw.Close()
		return nil, err
	}
	return &streamListener{raw: raw, listener: listener}, nil
}

type streamListener struct {
	raw      net.Listener
	listener *rendr.SessionListener
	once     sync.Once
	closeErr error
}

func (l *streamListener) Accept(ctx context.Context) (net.Conn, error) {
	return l.listener.AcceptStream(ctx)
}

func (l *streamListener) Close() error {
	if l == nil {
		return nil
	}
	l.once.Do(func() {
		l.closeErr = errors.Join(l.listener.Close(), l.raw.Close())
	})
	return l.closeErr
}

func (l *streamListener) Addr() net.Addr {
	if l == nil || l.raw == nil {
		return nil
	}
	return l.raw.Addr()
}

func buildTarget(compiled route.CompiledRoute) rendr.Target {
	children := make([]rendr.Target, 0, len(compiled.Leaves))
	for _, leaf := range compiled.Leaves {
		path := leaf.LogicalPath
		transport := path.LeafTransport
		if transport == "" {
			transport = chainTransport
		}
		children = append(children, rendr.Path(leaf.Name(), rendr.PathSpec{
			Transport: transport,
			Address:   leaf.ID,
			Opts: map[string]string{
				"name":                leaf.Name(),
				"logical_path_id":     path.ID,
				"expression":          path.Expression,
				"carrier_kind":        string(path.CarrierKind),
				"carrier_entry":       path.CarrierEntry.String(),
				"terminal_node_id":    leaf.TerminalNodeID.String(),
				"runtime_instance_id": leaf.ExpectedRuntimeInstanceID,
				"session_kind":        string(leaf.SessionKind),
			},
		}))
	}

	switch compiled.Target.Kind {
	case route.TargetRace:
		return rendr.Race("root", children)
	case route.TargetBond:
		return rendr.Bond("root", children)
	case route.TargetPeak:
		if len(children) == 1 {
			return rendr.Selector("root-peak", children)
		}
		peak := children[len(children)-1].Name()
		return rendr.Selector("root-peak", children, rendr.PeakTransfer{Targets: []string{peak}})
	default:
		return rendr.Selector("root", children)
	}
}
