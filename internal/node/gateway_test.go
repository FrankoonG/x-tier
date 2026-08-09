package node

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/FrankoonG/rendr"
	"github.com/FrankoonG/x-tier/internal/route"
)

func TestGatewayCarriesRendrHandshakeToTerminal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	addrB := freeAddr(t)
	addrC := freeAddr(t)
	addrE := freeAddr(t)
	rendrB := freeAddr(t)
	rendrC := freeAddr(t)
	rendrE := freeAddr(t)

	eNode, err := Start(ctx, Config{ID: "E", GatewayAddr: addrE, RendrAddr: rendrE, InstanceID: "inst-E", Peers: map[route.NodeID]Peer{}})
	if err != nil {
		t.Fatalf("start E: %v", err)
	}
	defer eNode.Close()
	cNode, err := Start(ctx, Config{
		ID:          "C",
		GatewayAddr: addrC,
		RendrAddr:   rendrC,
		InstanceID:  "inst-C",
		Peers: map[route.NodeID]Peer{
			"E": {ID: "E", GatewayAddr: addrE, Direction: route.DirectionOutbound},
		},
	})
	if err != nil {
		t.Fatalf("start C: %v", err)
	}
	defer cNode.Close()
	bNode, err := Start(ctx, Config{
		ID:          "B",
		GatewayAddr: addrB,
		RendrAddr:   rendrB,
		InstanceID:  "inst-B",
		Peers: map[route.NodeID]Peer{
			"C": {ID: "C", GatewayAddr: addrC, Direction: route.DirectionOutbound},
		},
	})
	if err != nil {
		t.Fatalf("start B: %v", err)
	}
	defer bNode.Close()

	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		conn, err := eNode.RendrListener().Accept(ctx)
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 32)
		n, err := conn.Read(buf)
		if err != nil {
			return
		}
		_, _ = conn.Write([]byte("echo:" + string(buf[:n])))
	}()

	topo := route.Topology{Local: "A"}
	for _, n := range []route.Node{
		{ID: "A", RendrCapable: true, InstanceID: "inst-A"},
		{ID: "B", RendrCapable: true, InstanceID: "inst-B"},
		{ID: "C", RendrCapable: true, InstanceID: "inst-C"},
		{ID: "E", RendrCapable: true, InstanceID: "inst-E"},
	} {
		topo.AddNode(n)
	}
	topo.AddEdge(route.Edge{From: "A", To: "B", Direction: route.DirectionOutbound, Enabled: true, NestedEnabled: true})
	topo.AddEdge(route.Edge{From: "B", To: "C", Direction: route.DirectionOutbound, Enabled: true, NestedEnabled: true})
	topo.AddEdge(route.Edge{From: "C", To: "E", Direction: route.DirectionOutbound, Enabled: true, NestedEnabled: true})
	compiled, err := route.Compile(topo, route.RouteIntent{Paths: []string{"B/C/E"}, Strategy: route.StrategySelector, EndpointKind: route.EndpointRendrStream})
	if err != nil {
		t.Fatalf("compile route: %v", err)
	}
	pathByExpr := map[string]route.ResolvedPath{}
	for _, p := range compiled.ResolvedPaths {
		pathByExpr[p.Expression] = p
	}
	firstGateway := map[route.NodeID]string{"B": bNode.GatewayAddr()}
	d := &rendr.Dialer{Root: compiled.Root}
	if err := d.AddStreamPathFactory("xtier-chain", func(ctx context.Context, addr string) (net.Conn, error) {
		p, ok := pathByExpr[addr]
		if !ok {
			t.Fatalf("unknown path addr %q", addr)
		}
		c, _, err := DialChain(ctx, "A", p, firstGateway[p.CarrierEntry])
		return c, err
	}); err != nil {
		t.Fatalf("add factory: %v", err)
	}
	conn, err := d.Dial(ctx)
	if err != nil {
		t.Fatalf("rendr dial through chain: %v", err)
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, "hello"); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 32)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got := string(buf[:n]); got != "echo:hello" {
		t.Fatalf("got %q, want echo:hello", got)
	}
	select {
	case <-serverDone:
	case <-time.After(time.Second):
		t.Fatal("server did not finish")
	}
}

func TestGatewayRejectsInboundOnlyNextHop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addrB := freeAddr(t)
	addrC := freeAddr(t)
	rendrB := freeAddr(t)
	rendrC := freeAddr(t)

	cNode, err := Start(ctx, Config{ID: "C", GatewayAddr: addrC, RendrAddr: rendrC, InstanceID: "inst-C", Peers: map[route.NodeID]Peer{}})
	if err != nil {
		t.Fatalf("start C: %v", err)
	}
	defer cNode.Close()
	bNode, err := Start(ctx, Config{
		ID:          "B",
		GatewayAddr: addrB,
		RendrAddr:   rendrB,
		InstanceID:  "inst-B",
		Peers: map[route.NodeID]Peer{
			"C": {ID: "C", GatewayAddr: addrC, Direction: route.DirectionInbound},
		},
	})
	if err != nil {
		t.Fatalf("start B: %v", err)
	}
	defer bNode.Close()

	path := route.ResolvedPath{
		ID:            "B-C",
		Expression:    "B/C",
		Hops:          []route.NodeID{"A", "B", "C"},
		CarrierEntry:  "B",
		RendrTerminal: "C",
		EndpointKind:  route.EndpointRendrStream,
	}
	_, res, err := DialChain(ctx, "A", path, bNode.GatewayAddr())
	if err == nil {
		t.Fatal("expected chain dial to fail")
	}
	if res.ErrorCode != "next_peer_not_outbound" {
		t.Fatalf("error code = %s, want next_peer_not_outbound (%v)", res.ErrorCode, err)
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}
