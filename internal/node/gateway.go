package node

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/FrankoonG/rendr"
	"github.com/FrankoonG/x-tier/internal/route"
	"github.com/FrankoonG/x-tier/internal/xraycarrier"
)

type Peer struct {
	ID          route.NodeID    `json:"id"`
	GatewayAddr string          `json:"gateway_addr"`
	RendrAddr   string          `json:"rendr_addr"`
	Direction   route.Direction `json:"direction"`
	XrayProfile string          `json:"xray_profile"`
}

type Config struct {
	ID          route.NodeID          `json:"id"`
	GatewayAddr string                `json:"gateway_addr"`
	RendrAddr   string                `json:"rendr_addr"`
	InstanceID  string                `json:"instance_id"`
	Carrier     string                `json:"carrier"`
	Peers       map[route.NodeID]Peer `json:"peers"`
}

type Node struct {
	cfg Config

	gateway net.Listener
	rendr   rendr.Listener
	xray    *xraycarrier.Dialer

	closeOnce sync.Once
	closed    chan struct{}
}

func Start(ctx context.Context, cfg Config) (*Node, error) {
	if cfg.ID == "" {
		return nil, errors.New("node: id is required")
	}
	gw, err := net.Listen("tcp", cfg.GatewayAddr)
	if err != nil {
		return nil, fmt.Errorf("listen gateway: %w", err)
	}
	rl, err := rendr.ListenTCP(cfg.RendrAddr)
	if err != nil {
		_ = gw.Close()
		return nil, fmt.Errorf("listen rendr: %w", err)
	}
	n := &Node{cfg: cfg, gateway: gw, rendr: rl, closed: make(chan struct{})}
	if cfg.Carrier == "xray-freedom" {
		xd, err := xraycarrier.NewFreedomDialer()
		if err != nil {
			_ = gw.Close()
			_ = rl.Close()
			return nil, fmt.Errorf("start xray freedom carrier: %w", err)
		}
		n.xray = xd
	}
	go n.acceptGateway()
	go func() {
		<-ctx.Done()
		_ = n.Close()
	}()
	return n, nil
}

func (n *Node) ID() route.NodeID {
	return n.cfg.ID
}

func (n *Node) GatewayAddr() string {
	return n.gateway.Addr().String()
}

func (n *Node) RendrAddr() string {
	return n.rendr.Addr().String()
}

func (n *Node) RendrListener() rendr.Listener {
	return n.rendr
}

func (n *Node) Close() error {
	var err error
	n.closeOnce.Do(func() {
		close(n.closed)
		err = n.gateway.Close()
		_ = n.rendr.Close()
		if n.xray != nil {
			n.xray.Close()
		}
	})
	return err
}

func (n *Node) acceptGateway() {
	for {
		c, err := n.gateway.Accept()
		if err != nil {
			select {
			case <-n.closed:
				return
			default:
				return
			}
		}
		go n.handleGateway(c)
	}
}

func (n *Node) handleGateway(upstream net.Conn) {
	defer upstream.Close()
	_ = upstream.SetDeadline(time.Now().Add(30 * time.Second))
	br := bufio.NewReader(upstream)
	line, err := br.ReadBytes('\n')
	if err != nil {
		return
	}
	var req OpenChainRequest
	if err := json.Unmarshal(line, &req); err != nil {
		_ = writeResult(upstream, OpenChainResult{OK: false, ErrorCode: "bad_request", ErrorMessage: err.Error(), FailedHop: n.cfg.ID})
		return
	}
	if req.CurrentNode != "" && req.CurrentNode != n.cfg.ID {
		_ = writeResult(upstream, OpenChainResult{OK: false, ErrorCode: "wrong_node", ErrorMessage: "request current node mismatch", FailedHop: n.cfg.ID})
		return
	}
	downstream, res, err := n.openNext(req)
	if err != nil {
		_ = writeResult(upstream, res)
		return
	}
	defer downstream.Close()
	if err := writeResult(upstream, res); err != nil {
		return
	}
	_ = upstream.SetDeadline(time.Time{})
	_ = downstream.SetDeadline(time.Time{})
	pipe(upstream, downstream)
}

func (n *Node) openNext(req OpenChainRequest) (net.Conn, OpenChainResult, error) {
	if len(req.Remaining) == 0 {
		return nil, OpenChainResult{OK: false, ErrorCode: "empty_remaining", ErrorMessage: "remaining hops is empty", FailedHop: n.cfg.ID}, errors.New("empty remaining")
	}
	if req.Remaining[0] != n.cfg.ID {
		return nil, OpenChainResult{OK: false, ErrorCode: "unexpected_hop", ErrorMessage: "first remaining hop is not current node", FailedHop: n.cfg.ID}, errors.New("unexpected hop")
	}
	if len(req.Remaining) == 1 {
		if req.Terminal != n.cfg.ID {
			return nil, OpenChainResult{OK: false, ErrorCode: "terminal_mismatch", ErrorMessage: "terminal mismatch at final hop", FailedHop: n.cfg.ID}, errors.New("terminal mismatch")
		}
		c, err := net.DialTimeout("tcp", n.rendr.Addr().String(), 10*time.Second)
		if err != nil {
			return nil, OpenChainResult{OK: false, ErrorCode: "terminal_dial_failed", ErrorMessage: err.Error(), FailedHop: n.cfg.ID}, err
		}
		return c, OpenChainResult{OK: true, FinalNodeID: n.cfg.ID, TerminalInstanceID: n.cfg.InstanceID}, nil
	}
	next := req.Remaining[1]
	peer, ok := n.cfg.Peers[next]
	if !ok {
		return nil, OpenChainResult{OK: false, ErrorCode: "next_peer_unknown", ErrorMessage: "next peer not configured", FailedHop: n.cfg.ID}, errors.New("next peer unknown")
	}
	if !peer.Direction.CanDialOutbound() {
		return nil, OpenChainResult{OK: false, ErrorCode: "next_peer_not_outbound", ErrorMessage: "next peer is not outbound", FailedHop: n.cfg.ID}, errors.New("next peer not outbound")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := n.dialCarrier(ctx, peer.GatewayAddr)
	if err != nil {
		return nil, OpenChainResult{OK: false, ErrorCode: "next_dial_failed", ErrorMessage: err.Error(), FailedHop: n.cfg.ID}, err
	}
	nextReq := req
	nextReq.CurrentNode = next
	nextReq.Remaining = append([]route.NodeID(nil), req.Remaining[1:]...)
	if err := writeRequest(c, nextReq); err != nil {
		_ = c.Close()
		return nil, OpenChainResult{OK: false, ErrorCode: "next_write_failed", ErrorMessage: err.Error(), FailedHop: n.cfg.ID}, err
	}
	res, err := readResult(c)
	if err != nil {
		_ = c.Close()
		return nil, OpenChainResult{OK: false, ErrorCode: "next_result_failed", ErrorMessage: err.Error(), FailedHop: n.cfg.ID}, err
	}
	if !res.OK {
		_ = c.Close()
		return nil, res, errors.New(res.ErrorCode)
	}
	return c, res, nil
}

func (n *Node) dialCarrier(ctx context.Context, addr string) (net.Conn, error) {
	if n.xray != nil {
		return n.xray.Dial(context.Background(), addr)
	}
	d := net.Dialer{}
	return d.DialContext(ctx, "tcp", addr)
}

func DialChain(ctx context.Context, origin route.NodeID, path route.ResolvedPath, firstGateway string) (net.Conn, OpenChainResult, error) {
	d := net.Dialer{}
	return DialChainWithDialer(ctx, origin, path, firstGateway, func(ctx context.Context, addr string) (net.Conn, error) {
		return d.DialContext(ctx, "tcp", addr)
	})
}

func DialChainWithDialer(ctx context.Context, origin route.NodeID, path route.ResolvedPath, firstGateway string, dial func(context.Context, string) (net.Conn, error)) (net.Conn, OpenChainResult, error) {
	d := net.Dialer{}
	if dial == nil {
		dial = func(ctx context.Context, addr string) (net.Conn, error) {
			return d.DialContext(ctx, "tcp", addr)
		}
	}
	c, err := dial(ctx, firstGateway)
	if err != nil {
		return nil, OpenChainResult{}, err
	}
	req := OpenChainRequest{
		Version:      1,
		RequestID:    path.ID,
		OriginNodeID: origin,
		CurrentNode:  path.CarrierEntry,
		Remaining:    append([]route.NodeID(nil), path.Hops[1:]...),
		Terminal:     path.RendrTerminal,
		EndpointKind: path.EndpointKind,
	}
	if err := writeRequest(c, req); err != nil {
		_ = c.Close()
		return nil, OpenChainResult{}, err
	}
	res, err := readResult(c)
	if err != nil {
		_ = c.Close()
		return nil, OpenChainResult{}, err
	}
	if !res.OK {
		_ = c.Close()
		return nil, res, fmt.Errorf("%s: %s", res.ErrorCode, res.ErrorMessage)
	}
	return c, res, nil
}

func writeRequest(w io.Writer, req OpenChainRequest) error {
	b, err := json.Marshal(req)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = w.Write(b)
	return err
}

func writeResult(w io.Writer, res OpenChainResult) error {
	b, err := json.Marshal(res)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = w.Write(b)
	return err
}

func readResult(r io.Reader) (OpenChainResult, error) {
	br := bufio.NewReader(r)
	line, err := br.ReadBytes('\n')
	if err != nil {
		return OpenChainResult{}, err
	}
	var res OpenChainResult
	if err := json.Unmarshal(line, &res); err != nil {
		return OpenChainResult{}, err
	}
	return res, nil
}

func pipe(a, b net.Conn) {
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(a, b)
		closeWrite(a)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(b, a)
		closeWrite(b)
		done <- struct{}{}
	}()
	<-done
	<-done
}

func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
}
