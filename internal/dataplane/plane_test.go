package dataplane

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FrankoonG/x-tier/internal/configstore"
	"github.com/FrankoonG/x-tier/internal/rendradapter"
	"github.com/FrankoonG/x-tier/internal/route"
	"github.com/FrankoonG/x-tier/internal/xraybridge"
	"github.com/FrankoonG/x-tier/internal/xrayconfig"
	"github.com/FrankoonG/x-tier/internal/xrayrt"
	xnet "github.com/xtls/xray-core/common/net"
	"golang.org/x/net/proxy"
)

const testVLESSUUID = "d342d11e-d424-4583-b36e-524ab1f0afa4"

func TestTwoPlanesCarryAuthenticatedSOCKSThroughRemoteXrayEgress(t *testing.T) {
	origin, originDone := startHalfCloseOrigin(t)
	bCarrier := reserveAddress(t)
	aSOCKS := reserveAddress(t)

	b, err := Start(context.Background(), terminalConfig(bCarrier))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closePlane(t, b) })
	a, err := Start(context.Background(), entryConfig(aSOCKS, bCarrier))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closePlane(t, a) })

	dialer, err := proxy.SOCKS5("tcp", aSOCKS, &proxy.Auth{User: "terminal", Password: "entry-secret"}, proxy.Direct)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := dialer.Dial("tcp", origin)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	payload := make([]byte, 2<<20)
	for index := range payload {
		payload[index] = byte(index * 29)
	}
	want := sha256.Sum256(payload)
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	closeWriter, ok := conn.(interface{ CloseWrite() error })
	if !ok {
		t.Fatalf("SOCKS connection %T does not support CloseWrite", conn)
	}
	if err := closeWriter.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	response, err := io.ReadAll(conn)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(response); got != hex.EncodeToString(want[:]) {
		t.Fatalf("origin response = %q, want %x", got, want)
	}
	select {
	case got := <-originDone:
		if got != want {
			t.Fatalf("origin hash = %x, want %x", got, want)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("origin did not observe request EOF")
	}

	aStatus := a.Status()
	bStatus := b.Status()
	if aStatus.AppliedRevision != 1 || aStatus.Rendr.TotalClient != 1 {
		t.Fatalf("entry status = %+v", aStatus)
	}
	if bStatus.AppliedRevision != 1 || bStatus.Rendr.TotalAccepted != 1 {
		t.Fatalf("terminal status = %+v", bStatus)
	}
	assertBoundListener(t, aStatus, "xtier-user-socks", aSOCKS)
	assertBoundListener(t, bStatus, "xtier-node-vless", bCarrier)
}

func TestTwoPlanesPropagateOriginCloseBeforeSOCKSClientClose(t *testing.T) {
	originListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = originListener.Close() })
	originDone := make(chan error, 1)
	go func() {
		conn, acceptErr := originListener.Accept()
		if acceptErr != nil {
			originDone <- acceptErr
			return
		}
		request := make([]byte, len("request"))
		_, readErr := io.ReadFull(conn, request)
		if readErr == nil && string(request) != "request" {
			readErr = fmt.Errorf("origin request = %q", request)
		}
		if readErr == nil {
			_, readErr = conn.Write([]byte("response"))
		}
		originDone <- errors.Join(readErr, conn.Close())
	}()

	bCarrier := reserveAddress(t)
	aSOCKS := reserveAddress(t)
	b, err := Start(context.Background(), terminalConfig(bCarrier))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closePlane(t, b) })
	a, err := Start(context.Background(), entryConfig(aSOCKS, bCarrier))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closePlane(t, a) })

	dialer, err := proxy.SOCKS5("tcp", aSOCKS, &proxy.Auth{User: "terminal", Password: "entry-secret"}, proxy.Direct)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := dialer.Dial("tcp", originListener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("request")); err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	response := make([]byte, len("response"))
	if _, err := io.ReadFull(conn, response); err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	if string(response) != "response" {
		_ = conn.Close()
		t.Fatalf("response = %q", response)
	}
	terminalRead := make(chan error, 1)
	go func() {
		var one [1]byte
		_, readErr := conn.Read(one[:])
		terminalRead <- readErr
	}()
	select {
	case readErr := <-terminalRead:
		if !errors.Is(readErr, io.EOF) {
			_ = conn.Close()
			t.Fatalf("terminal read = %v, want EOF", readErr)
		}
	case <-time.After(10 * time.Second):
		_ = conn.Close()
		t.Fatal("origin close did not reach the SOCKS client")
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-originDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("origin did not finish")
	}
}

func TestApplyBindFailureRestoresPreviousInboundAndAppliedRevision(t *testing.T) {
	origin := startEchoOrigin(t)
	bCarrier := reserveAddress(t)
	oldSOCKS := reserveAddress(t)
	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	newSOCKS := blocker.Addr().String()

	b, err := Start(context.Background(), terminalConfig(bCarrier))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closePlane(t, b) })
	a, err := Start(context.Background(), entryConfig(oldSOCKS, bCarrier))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closePlane(t, a) })
	assertSOCKSEcho(t, oldSOCKS, origin)

	candidate := entryConfig(newSOCKS, bCarrier)
	candidate.Revision = 2
	if err := a.Apply(context.Background(), candidate); err == nil {
		t.Fatal("bind-conflicting configuration applied")
	}
	status := a.Status()
	if status.State != "degraded" || status.AppliedRevision != 1 || status.AttemptedRevision != 2 || status.LastError == "" {
		t.Fatalf("failed apply status = %+v", status)
	}
	assertBoundListener(t, status, "xtier-user-socks", oldSOCKS)
	assertSOCKSEcho(t, oldSOCKS, origin)

	if err := blocker.Close(); err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(context.Background(), candidate); err != nil {
		t.Fatal(err)
	}
	status = a.Status()
	if status.State != "running" || status.AppliedRevision != 2 || status.LastError != "" {
		t.Fatalf("recovered apply status = %+v", status)
	}
	assertBoundListener(t, status, "xtier-user-socks", newSOCKS)
	assertSOCKSEcho(t, newSOCKS, origin)
	if conn, err := net.DialTimeout("tcp", oldSOCKS, 250*time.Millisecond); err == nil {
		_ = conn.Close()
		t.Fatal("old SOCKS listener remained reachable after successful replacement")
	}
}

func TestDisabledExitPeerCandidateIsRejectedWithoutDisruptingSOCKS(t *testing.T) {
	origin := startEchoOrigin(t)
	bCarrier := reserveAddress(t)
	aSOCKS := reserveAddress(t)

	b, err := Start(context.Background(), terminalConfig(bCarrier))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closePlane(t, b) })
	a, err := Start(context.Background(), entryConfig(aSOCKS, bCarrier))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closePlane(t, a) })

	candidate := entryConfig(aSOCKS, bCarrier)
	candidate.Revision = 2
	candidate.Peers[0].Enabled = false
	if err := a.Apply(context.Background(), candidate); err == nil || !strings.Contains(err.Error(), "config.inbound_exit_peer_unavailable") {
		t.Fatalf("disabled exit peer candidate error = %v", err)
	}
	status := a.Status()
	if status.AppliedRevision != 1 || status.AttemptedRevision != 2 || status.LastError == "" {
		t.Fatalf("rejected candidate status = %+v", status)
	}
	assertBoundListener(t, status, "xtier-user-socks", aSOCKS)
	assertSOCKSEcho(t, aSOCKS, origin)
}

func TestRevokingInboundPeerClosesExistingCarrierAndRejectsNewFlows(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*configstore.Config)
	}{
		{name: "disable", mutate: func(cfg *configstore.Config) { cfg.Peers[0].Enabled = false }},
		{name: "rotate credential", mutate: func(cfg *configstore.Config) {
			profile := cfg.XrayProfiles["carrier-in"]
			profile.VLESS.UUID = "7f6ec3bb-d3bd-420f-a083-8b850bb2fe56"
			cfg.XrayProfiles["carrier-in"] = profile
		}},
		{name: "remove inbound direction", mutate: func(cfg *configstore.Config) {
			cfg.Peers[0].Direction = route.DirectionOutbound
			cfg.Peers[0].GatewayAddr = "127.0.0.1:2443"
		}},
		{name: "delete peer", mutate: func(cfg *configstore.Config) { cfg.Peers = nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			origin, received := startObservedEchoOrigin(t)
			bCarrier := reserveAddress(t)
			aSOCKS := reserveAddress(t)
			b, err := Start(context.Background(), terminalConfig(bCarrier))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { closePlane(t, b) })
			a, err := Start(context.Background(), entryConfig(aSOCKS, bCarrier))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { closePlane(t, a) })

			dialer, err := proxy.SOCKS5("tcp", aSOCKS, &proxy.Auth{User: "terminal", Password: "entry-secret"}, proxy.Direct)
			if err != nil {
				t.Fatal(err)
			}
			conn, err := dialer.Dial("tcp", origin)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			before := []byte("before-revocation")
			if _, err := conn.Write(before); err != nil {
				t.Fatal(err)
			}
			echo := make([]byte, len(before))
			if _, err := io.ReadFull(conn, echo); err != nil || string(echo) != string(before) {
				t.Fatalf("pre-revocation echo=%q err=%v", echo, err)
			}

			revoked := terminalConfig(bCarrier)
			revoked.Revision = 2
			test.mutate(&revoked)
			if err := b.Apply(context.Background(), revoked); err != nil {
				t.Fatal(err)
			}
			_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
			after := []byte("after-revocation")
			_, writeErr := conn.Write(after)
			postEcho := make([]byte, len(after))
			_, readErr := io.ReadFull(conn, postEcho)
			if writeErr == nil && readErr == nil {
				t.Fatalf("revoked carrier echoed post-revocation data: %q", postEcho)
			}
			select {
			case all := <-received:
				if string(all) != string(before) {
					t.Fatalf("origin received data after revocation: %q", all)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("origin did not observe revoked carrier closure")
			}
			restored := terminalConfig(bCarrier)
			restored.Revision = 3
			if err := b.Apply(context.Background(), restored); err != nil {
				t.Fatalf("restore admitted peer for bounded cleanup: %v", err)
			}
		})
	}
}

func TestCarrierAdmissionLimitsAreFailClosed(t *testing.T) {
	tests := []struct {
		name     string
		active   int
		peerUsed int
	}{
		{name: "global", active: rendradapter.MaxAcceptedSessions, peerUsed: 1},
		{name: "per peer", active: maxActiveCarriersPerPeer, peerUsed: maxActiveCarriersPerPeer},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			p := &Plane{
				activeCarriers: make(map[*closeObservedConn]carrierAuthorization, test.active),
				carrierCounts:  map[string]int{"node-a": test.peerUsed},
			}
			p.routes.Store(&routeTable{carrierPeers: map[string]string{"account-a": "node-a"}})
			for index := 0; index < test.active; index++ {
				peer := fmt.Sprintf("other-%d", index)
				if index < test.peerUsed {
					peer = "node-a"
				}
				p.activeCarriers[&closeObservedConn{}] = carrierAuthorization{peerNodeID: peer}
			}
			left, right := net.Pipe()
			defer left.Close()
			defer right.Close()
			err := p.Handoff(context.Background(), xraybridge.CarrierRequest{
				InboundTag: xrayconfig.NodeVLESSTag, AuthenticatedUser: "account-a",
			}, left)
			if !errors.Is(err, ErrCarrierAdmissionLimit) {
				t.Fatalf("Handoff error = %v, want admission limit", err)
			}
		})
	}
}

func TestApplyRejectsRevisionRollbackWithoutChangingRuntime(t *testing.T) {
	bCarrier := reserveAddress(t)
	aSOCKS := reserveAddress(t)
	a, err := Start(context.Background(), entryConfig(aSOCKS, bCarrier))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closePlane(t, a) })

	newer := entryConfig(aSOCKS, bCarrier)
	newer.Revision = 2
	newer.System.LogLevel = "debug"
	if err := a.Apply(context.Background(), newer); err != nil {
		t.Fatal(err)
	}
	before := a.Status()
	stale := entryConfig(aSOCKS, bCarrier)
	stale.Revision = 1
	stale.XrayProfiles["terminal-socks"].SOCKS.Password = "revoked-password"
	if err := a.Apply(context.Background(), stale); err == nil {
		t.Fatal("stale revision unexpectedly applied")
	}
	after := a.Status()
	if after.State != "degraded" || after.AppliedRevision != 2 || after.AttemptedRevision != 1 ||
		!strings.Contains(after.LastError, "dataplane.revision_regression") ||
		after.LastErrorCode != "dataplane.revision_regression" ||
		after.Xray.Current == nil || before.Xray.Current == nil || after.Xray.Current.Generation != before.Xray.Current.Generation {
		t.Fatalf("stale apply changed runtime status: before=%+v after=%+v", before, after)
	}
}

func TestApplyRejectsSameRevisionContentMismatchAndRestoresIdempotently(t *testing.T) {
	bCarrier := reserveAddress(t)
	aSOCKS := reserveAddress(t)
	original := entryConfig(aSOCKS, bCarrier)
	a, err := Start(context.Background(), original)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closePlane(t, a) })

	before := a.Status()
	drifted := original
	drifted.System.LogLevel = "debug"
	if err := a.Apply(context.Background(), drifted); err == nil || !strings.Contains(err.Error(), "dataplane.revision_content_mismatch") {
		t.Fatalf("same-revision drift error=%v", err)
	}
	failed := a.Status()
	if failed.State != "degraded" || failed.AppliedRevision != original.Revision ||
		failed.AttemptedRevision != original.Revision || failed.AppliedDigest != before.AppliedDigest ||
		failed.AttemptedDigest == failed.AppliedDigest ||
		!strings.Contains(failed.LastError, "dataplane.revision_content_mismatch") ||
		failed.LastErrorCode != "dataplane.revision_content_mismatch" ||
		failed.Xray.Current == nil || before.Xray.Current == nil ||
		failed.Xray.Current.Generation != before.Xray.Current.Generation {
		t.Fatalf("same-revision drift changed runtime: before=%+v failed=%+v", before, failed)
	}

	if err := a.Apply(context.Background(), original); err != nil {
		t.Fatalf("restore identical applied content: %v", err)
	}
	restored := a.Status()
	if restored.State != "running" || restored.LastError != "" || restored.LastErrorCode != "" ||
		restored.AppliedDigest != before.AppliedDigest || restored.AttemptedDigest != before.AppliedDigest ||
		restored.Xray.Current == nil || restored.Xray.Current.Generation != before.Xray.Current.Generation {
		t.Fatalf("idempotent restore changed generation or retained failure: before=%+v restored=%+v", before, restored)
	}
}

func TestApplySameRevisionReplacesFailedRendrRuntime(t *testing.T) {
	cfg := configstore.DefaultConfig()
	plane, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	original := plane.currentRendr()
	if err := closeRendrRuntime(original); err != nil {
		t.Fatalf("close original rendr runtime: %v", err)
	}
	failed := newFakeRendrRuntime("failed")
	replacement := newFakeRendrRuntime("running")
	plane.lifecycleMu.Lock()
	plane.rendr = failed
	plane.rendrFactory = func(context.Context) (rendrRuntime, error) { return replacement, nil }
	plane.lifecycleMu.Unlock()

	if err := plane.Apply(context.Background(), cfg); err != nil {
		t.Fatalf("same-revision recovery: %v", err)
	}
	if got := plane.currentRendr(); got != replacement {
		t.Fatalf("active rendr runtime = %T %p, want replacement %p", got, got, replacement)
	}
	if replacement.setDialersCalls != 1 {
		t.Fatalf("replacement SetDialers calls = %d, want 1", replacement.setDialersCalls)
	}
	select {
	case <-failed.closeStarted:
	case <-time.After(time.Second):
		t.Fatal("failed rendr runtime was not retired")
	}
	deadline := time.Now().Add(time.Second)
	for {
		retirements, retirementErr := plane.rendrRetirementSnapshot()
		if retirementErr != nil {
			t.Fatalf("rendr retirement error=%v", retirementErr)
		}
		if len(retirements) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("completed rendr retirement remained retained")
		}
		time.Sleep(time.Millisecond)
	}
	if status := plane.Status(); status.State != "running" || status.Rendr.State != "running" {
		t.Fatalf("recovered plane status = %+v", status)
	}
	if err := plane.Close(); err != nil {
		t.Fatalf("close recovered plane: %v", err)
	}
}

func TestRendrRecoveryRejectsFailedReplacementAndAppliesRestartBackoff(t *testing.T) {
	cfg := configstore.DefaultConfig()
	plane, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	original := plane.currentRendr()
	if err := closeRendrRuntime(original); err != nil {
		t.Fatal(err)
	}
	failed := newFakeRendrRuntime("failed")
	plane.lifecycleMu.Lock()
	plane.rendr = failed
	factoryCalls := 0
	plane.rendrFactory = func(context.Context) (rendrRuntime, error) {
		factoryCalls++
		return newFakeRendrRuntime("failed"), nil
	}
	plane.lifecycleMu.Unlock()
	firstErr := plane.Apply(context.Background(), cfg)
	if firstErr == nil || !strings.Contains(firstErr.Error(), "restarted rendr runtime is failed") {
		t.Fatalf("failed replacement error=%v", firstErr)
	}
	secondErr := plane.Apply(context.Background(), cfg)
	if secondErr == nil || !strings.Contains(secondErr.Error(), "restart backoff active") {
		t.Fatalf("restart backoff error=%v", secondErr)
	}
	if factoryCalls != 1 || plane.currentRendr() != failed {
		t.Fatalf("recovery churned runtimes: factory_calls=%d current=%p failed=%p", factoryCalls, plane.currentRendr(), failed)
	}
	closePlane(t, plane)
}

func TestFailStopRevokesCarriersClosesInboundAndSameRevisionRecovers(t *testing.T) {
	carrierAddress := reserveAddress(t)
	cfg := terminalConfig(carrierAddress)
	plane, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	original := plane.currentRendr()
	if err := closeRendrRuntime(original); err != nil {
		t.Fatal(err)
	}
	runtime := newFakeRendrRuntime("running")
	runtime.acceptCarrier = true
	runtime.injected = make(chan net.Conn, 1)
	plane.lifecycleMu.Lock()
	plane.rendr = runtime
	plane.lifecycleMu.Unlock()

	account := singleCarrierAccount(t, cfg)
	left, right := net.Pipe()
	defer right.Close()
	handoffDone := make(chan error, 1)
	go func() {
		handoffDone <- plane.Handoff(context.Background(), xraybridge.CarrierRequest{
			InboundTag: xrayconfig.NodeVLESSTag, AuthenticatedUser: account,
		}, left)
	}()
	select {
	case <-runtime.injected:
	case <-time.After(time.Second):
		t.Fatal("carrier was not injected into the active rendr runtime")
	}
	if err := plane.FailStop(context.Background(), "config.read_failed_fail_closed"); err != nil {
		t.Fatal(err)
	}
	failStopped := plane.Status()
	if failStopped.LastErrorCode != "config.read_failed_fail_closed" {
		t.Fatalf("fail-stop error code=%q", failStopped.LastErrorCode)
	}
	select {
	case err := <-handoffDone:
		if err != nil {
			t.Fatalf("revoked carrier handoff error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("fail-stop did not revoke the active carrier")
	}
	plane.carrierMu.Lock()
	active, peerCounts := len(plane.activeCarriers), len(plane.carrierCounts)
	plane.carrierMu.Unlock()
	if active != 0 || peerCounts != 0 {
		t.Fatalf("carrier accounting after fail-stop: active=%d peers=%d", active, peerCounts)
	}
	if conn, dialErr := net.DialTimeout("tcp", carrierAddress, 100*time.Millisecond); dialErr == nil {
		_ = conn.Close()
		t.Fatal("managed node inbound remained reachable after fail-stop")
	}
	newLeft, newRight := net.Pipe()
	defer newLeft.Close()
	defer newRight.Close()
	if err := plane.Handoff(context.Background(), xraybridge.CarrierRequest{
		InboundTag: xrayconfig.NodeVLESSTag, AuthenticatedUser: account,
	}, newLeft); err == nil || !strings.Contains(err.Error(), "not authorized") {
		t.Fatalf("new carrier after fail-stop error=%v", err)
	}
	if err := plane.Apply(context.Background(), cfg); err != nil {
		t.Fatalf("same-revision recovery: %v", err)
	}
	status := plane.Status()
	if status.State != "running" || status.LastError != "" {
		t.Fatalf("recovered status=%+v", status)
	}
	assertBoundListener(t, status, xrayconfig.NodeVLESSTag, carrierAddress)
	closePlane(t, plane)
}

func TestFailStopDoesNotWaitForBlockedUserDial(t *testing.T) {
	peerAddress := reserveAddress(t)
	socksAddress := reserveAddress(t)
	cfg := entryConfig(socksAddress, peerAddress)
	plane, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	original := plane.currentRendr()
	if err := closeRendrRuntime(original); err != nil {
		t.Fatal(err)
	}
	runtime := newFakeRendrRuntime("running")
	runtime.dialStarted = make(chan struct{})
	plane.lifecycleMu.Lock()
	plane.rendr = runtime
	plane.lifecycleMu.Unlock()

	dialCtx, cancelDial := context.WithCancel(context.Background())
	dialDone := make(chan error, 1)
	go func() {
		_, dialErr := plane.Dial(dialCtx, xraybridge.UserRequest{
			InboundTag: xrayconfig.UserSOCKSTag,
			Target:     xnet.TCPDestination(xnet.DomainAddress("blocked.example.test"), 443),
		})
		dialDone <- dialErr
	}()
	select {
	case <-runtime.dialStarted:
	case <-time.After(time.Second):
		t.Fatal("user dial did not enter the blocked runtime")
	}
	stopCtx, cancelStop := context.WithTimeout(context.Background(), 500*time.Millisecond)
	started := time.Now()
	err = plane.FailStop(stopCtx, "config.read_failed_fail_closed")
	cancelStop()
	if err != nil {
		t.Fatalf("fail-stop behind blocked Dial: %v", err)
	}
	if elapsed := time.Since(started); elapsed >= 400*time.Millisecond {
		t.Fatalf("blocked Dial held the apply lock for %s", elapsed)
	}
	cancelDial()
	select {
	case err := <-dialDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("blocked Dial error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked Dial did not honor cancellation")
	}
	closePlane(t, plane)
}

func TestFailStoppedStatusRemainsNonAppliedDuringSameRevisionRecovery(t *testing.T) {
	carrierAddress := reserveAddress(t)
	cfg := terminalConfig(carrierAddress)
	plane, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := plane.FailStop(context.Background(), "config.read_failed_fail_closed"); err != nil {
		t.Fatal(err)
	}
	plane.applyMu.Lock()
	applyDone := make(chan error, 1)
	go func() { applyDone <- plane.Apply(context.Background(), cfg) }()
	deadline := time.Now().Add(time.Second)
	var applying Status
	for time.Now().Before(deadline) {
		applying = plane.Status()
		if applying.State == "applying" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if applying.State != "applying" || !applying.FailStopped || applying.ObservationFresh {
		plane.applyMu.Unlock()
		t.Fatalf("fail-stop recovery status=%+v", applying)
	}
	plane.applyMu.Unlock()
	if err := <-applyDone; err != nil {
		t.Fatal(err)
	}
	recovered := plane.Status()
	if recovered.State != "running" || recovered.FailStopped || recovered.LastError != "" || recovered.LastErrorCode != "" {
		t.Fatalf("recovered status=%+v", recovered)
	}
	closePlane(t, plane)
}

func TestLateApplyCannotCommitRunningStateAfterCloseBegins(t *testing.T) {
	cfg := configstore.DefaultConfig()
	plane, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	candidate := cfg
	candidate.Revision++
	plane.applyMu.Lock()
	applyDone := make(chan error, 1)
	go func() { applyDone <- plane.Apply(context.Background(), candidate) }()
	deadline := time.Now().Add(time.Second)
	applyingObserved := false
	for time.Now().Before(deadline) {
		if status := plane.Status(); status.State == "applying" {
			applyingObserved = true
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !applyingObserved {
		plane.applyMu.Unlock()
		t.Fatal("Apply did not reach the blocked publish phase")
	}
	plane.beginClose()
	plane.applyMu.Unlock()
	select {
	case err := <-applyDone:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("late Apply error=%v, want closed lifecycle", err)
		}
	case <-time.After(time.Second):
		t.Fatal("late Apply remained blocked")
	}
	if status := plane.Status(); status.State == "running" {
		t.Fatalf("late Apply restored running state after close: %+v", status)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := plane.CloseContext(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestHandoffCancellationClosesTransferredCarrier(t *testing.T) {
	carrierAddress := reserveAddress(t)
	cfg := terminalConfig(carrierAddress)
	plane, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	original := plane.currentRendr()
	if err := closeRendrRuntime(original); err != nil {
		t.Fatal(err)
	}
	runtime := newFakeRendrRuntime("running")
	runtime.acceptCarrier = true
	runtime.injected = make(chan net.Conn, 1)
	plane.lifecycleMu.Lock()
	plane.rendr = runtime
	plane.lifecycleMu.Unlock()
	left, right := net.Pipe()
	defer right.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	account := singleCarrierAccount(t, cfg)
	go func() {
		done <- plane.Handoff(ctx, xraybridge.CarrierRequest{
			InboundTag: xrayconfig.NodeVLESSTag, AuthenticatedUser: account,
		}, left)
	}()
	select {
	case <-runtime.injected:
	case <-time.After(time.Second):
		t.Fatal("carrier was not transferred")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("handoff error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("handoff ignored cancellation")
	}
	if err := right.SetReadDeadline(time.Now().Add(time.Second)); err == nil {
		var one [1]byte
		if _, err := right.Read(one[:]); err == nil {
			t.Fatal("transferred carrier remained open after handoff cancellation")
		}
	}
	plane.carrierMu.Lock()
	active, peerCounts := len(plane.activeCarriers), len(plane.carrierCounts)
	plane.carrierMu.Unlock()
	if active != 0 || peerCounts != 0 {
		t.Fatalf("carrier accounting after cancellation: active=%d peers=%d", active, peerCounts)
	}
	closePlane(t, plane)
}

func TestRendrSelfHealRevokesOldRuntimeCarriersAndReleasesAccounting(t *testing.T) {
	carrierAddress := reserveAddress(t)
	cfg := terminalConfig(carrierAddress)
	plane, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	original := plane.currentRendr()
	if err := closeRendrRuntime(original); err != nil {
		t.Fatal(err)
	}
	failed := newFakeRendrRuntime("running")
	failed.acceptCarrier = true
	failed.injected = make(chan net.Conn, 1)
	replacement := newFakeRendrRuntime("running")
	plane.lifecycleMu.Lock()
	plane.rendr = failed
	plane.rendrFactory = func(context.Context) (rendrRuntime, error) { return replacement, nil }
	plane.lifecycleMu.Unlock()

	left, right := net.Pipe()
	defer right.Close()
	handoffDone := make(chan error, 1)
	account := singleCarrierAccount(t, cfg)
	go func() {
		handoffDone <- plane.Handoff(context.Background(), xraybridge.CarrierRequest{
			InboundTag: xrayconfig.NodeVLESSTag, AuthenticatedUser: account,
		}, left)
	}()
	select {
	case <-failed.injected:
	case <-time.After(time.Second):
		t.Fatal("carrier was not injected into the old rendr runtime")
	}
	failed.mu.Lock()
	failed.state = "failed"
	failed.mu.Unlock()
	if err := plane.Apply(context.Background(), cfg); err != nil {
		t.Fatalf("self-heal: %v", err)
	}
	select {
	case err := <-handoffDone:
		if err != nil {
			t.Fatalf("old carrier handoff error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("self-heal left the old runtime carrier active")
	}
	plane.carrierMu.Lock()
	active, peerCounts := len(plane.activeCarriers), len(plane.carrierCounts)
	plane.carrierMu.Unlock()
	if active != 0 || peerCounts != 0 {
		t.Fatalf("carrier accounting after self-heal: active=%d peers=%d", active, peerCounts)
	}
	select {
	case <-failed.closeStarted:
	case <-time.After(time.Second):
		t.Fatal("old rendr runtime retirement did not start")
	}
	closePlane(t, plane)
}

func TestPlaneCloseReportsBlockedRetiredRendrRuntime(t *testing.T) {
	cfg := configstore.DefaultConfig()
	plane, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	original := plane.currentRendr()
	if err := closeRendrRuntime(original); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	retired := newFakeRendrRuntime("failed")
	retired.closeRelease = release
	replacement := newFakeRendrRuntime("running")
	plane.lifecycleMu.Lock()
	plane.rendr = retired
	plane.rendrFactory = func(context.Context) (rendrRuntime, error) { return replacement, nil }
	plane.lifecycleMu.Unlock()
	if err := plane.Apply(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	select {
	case <-retired.closeStarted:
	case <-time.After(time.Second):
		t.Fatal("retired rendr runtime did not begin closing")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	err = plane.CloseContext(ctx)
	cancel()
	if !errors.Is(err, xrayrt.ErrShutdownIncomplete) || !errors.Is(err, context.DeadlineExceeded) {
		close(release)
		t.Fatalf("CloseContext error=%v, want blocked retirement evidence", err)
	}
	close(release)
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := plane.CloseContext(ctx); err != nil {
		t.Fatalf("close after retirement release: %v", err)
	}
}

func TestPlaneCloseContextBoundsBlockedRendrShutdown(t *testing.T) {
	cfg := configstore.DefaultConfig()
	plane, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	original := plane.currentRendr()
	if err := closeRendrRuntime(original); err != nil {
		t.Fatalf("close original rendr runtime: %v", err)
	}
	release := make(chan struct{})
	blocked := newFakeRendrRuntime("running")
	blocked.closeRelease = release
	plane.lifecycleMu.Lock()
	plane.rendr = blocked
	plane.lifecycleMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	started := time.Now()
	err = plane.CloseContext(ctx)
	cancel()
	if !errors.Is(err, xrayrt.ErrShutdownIncomplete) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("CloseContext error = %v, want incomplete deadline", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("CloseContext exceeded deadline by too much: %s", elapsed)
	}
	select {
	case <-blocked.closeStarted:
	case <-time.After(time.Second):
		t.Fatal("rendr shutdown did not start")
	}
	forceStarted := time.Now()
	if err := plane.ForceClose(); !errors.Is(err, xrayrt.ErrShutdownIncomplete) {
		t.Fatalf("ForceClose error = %v, want incomplete shutdown", err)
	}
	if elapsed := time.Since(forceStarted); elapsed > time.Second {
		t.Fatalf("ForceClose blocked for %s", elapsed)
	}
	close(release)
}

func TestPlaneForceCloseCanInterruptBlockedGracefulClose(t *testing.T) {
	cfg := configstore.DefaultConfig()
	plane, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	original := plane.currentRendr()
	if err := closeRendrRuntime(original); err != nil {
		t.Fatalf("close original rendr runtime: %v", err)
	}
	release := make(chan struct{})
	blocked := newFakeRendrRuntime("running")
	blocked.closeRelease = release
	plane.lifecycleMu.Lock()
	plane.rendr = blocked
	plane.lifecycleMu.Unlock()

	gracefulResult := make(chan error, 1)
	go func() { gracefulResult <- plane.CloseContext(context.Background()) }()
	select {
	case <-blocked.closeStarted:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("graceful Rendr shutdown did not start")
	}

	forceResult := make(chan error, 1)
	go func() { forceResult <- plane.ForceClose() }()
	select {
	case err := <-forceResult:
		if !errors.Is(err, xrayrt.ErrShutdownIncomplete) {
			close(release)
			t.Fatalf("ForceClose error = %v, want incomplete Rendr evidence", err)
		}
	case <-time.After(time.Second):
		close(release)
		t.Fatal("ForceClose waited behind the blocked graceful-close mutex")
	}
	close(release)
	select {
	case <-gracefulResult:
	case <-time.After(time.Second):
		t.Fatal("graceful close did not return after Rendr release")
	}
}

func TestStatusPreservesObservedAtUntilReconcileTransition(t *testing.T) {
	bCarrier := reserveAddress(t)
	aSOCKS := reserveAddress(t)
	a, err := Start(context.Background(), entryConfig(aSOCKS, bCarrier))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closePlane(t, a) })

	first := a.Status()
	time.Sleep(20 * time.Millisecond)
	second := a.Status()
	if !first.ObservationFresh || !second.ObservationFresh || first.ObservedAt.IsZero() || !second.ObservedAt.Equal(first.ObservedAt) {
		t.Fatalf("status read changed reconciliation observation time: first=%s second=%s", first.ObservedAt, second.ObservedAt)
	}
	a.reconcileMu.Lock()
	stale := a.Status()
	a.reconcileMu.Unlock()
	if stale.ObservationFresh || !stale.ObservedAt.Equal(second.ObservedAt) {
		t.Fatalf("contended status did not expose stale observation: %+v", stale)
	}
}

func TestFailedReconcileRedactsCredentialFromRuntimeStatus(t *testing.T) {
	bCarrier := reserveAddress(t)
	aSOCKS := reserveAddress(t)
	a, err := Start(context.Background(), entryConfig(aSOCKS, bCarrier))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closePlane(t, a) })

	candidate := entryConfig(aSOCKS, bCarrier)
	candidate.Revision = 2
	candidate.XrayProfiles["carrier-b"] = configstore.XrayProfile{
		ID: "carrier-b", Kind: "vless", VLESS: &configstore.VLESSProfile{
			UUID: "credential-that-must-not-escape", Transport: "tcp", Security: "none", AllowInsecurePlaintext: true,
		},
	}
	if err := a.Apply(context.Background(), candidate); err == nil {
		t.Fatal("invalid credential unexpectedly applied")
	}
	status := a.Status()
	if status.LastError == "" || strings.Contains(status.LastError, "credential-that-must-not-escape") {
		t.Fatalf("runtime error was not redacted: %q", status.LastError)
	}
}

func assertBoundListener(t *testing.T, status Status, tag, address string) {
	assertListenerState(t, status, tag, address, "bound")
}

func assertListenerState(t *testing.T, status Status, tag, address, state string) {
	t.Helper()
	for _, listener := range status.Listeners {
		if listener.Tag == tag {
			if listener.State != state || listener.Listen != address {
				t.Fatalf("listener %s = %+v, want %s at %s", tag, listener, state, address)
			}
			return
		}
	}
	t.Fatalf("listener %s missing from status: %+v", tag, status.Listeners)
}

func entryConfig(socksAddress, peerAddress string) configstore.Config {
	cfg := configstore.DefaultConfig()
	cfg.Revision = 1
	cfg.XrayProfiles = map[string]configstore.XrayProfile{
		"carrier-b": vlessProfile("carrier-b"),
		"terminal-socks": {
			ID:   "terminal-socks",
			Kind: "socks",
			SOCKS: &configstore.SOCKSProfile{
				Username: "terminal",
				Password: "entry-secret",
			},
		},
	}
	cfg.Peers = []configstore.PeerConfig{{
		Name:          "B",
		NodeID:        "node-0000000000000000000000000000000b",
		Direction:     route.DirectionOutbound,
		GatewayAddr:   peerAddress,
		XrayProfileID: "carrier-b",
		Enabled:       true,
		RendrCapable:  true,
	}}
	cfg.NodeInbound = []configstore.InboundConfig{{
		Kind:          "socks",
		Purpose:       "user",
		Listen:        socksAddress,
		Enabled:       true,
		XrayProfileID: "terminal-socks",
		ExitPeer:      "B",
	}}
	return cfg
}

func terminalConfig(carrierAddress string) configstore.Config {
	cfg := configstore.DefaultConfig()
	cfg.Revision = 1
	cfg.XrayProfiles = map[string]configstore.XrayProfile{
		"carrier-in": vlessProfile("carrier-in"),
	}
	cfg.Peers = []configstore.PeerConfig{{
		Name: "A", NodeID: "node-0000000000000000000000000000000a",
		Direction: route.DirectionInbound, XrayProfileID: "carrier-in",
		Enabled: true, RendrCapable: true,
	}}
	cfg.NodeInbound = []configstore.InboundConfig{{
		Kind:    "node-vless",
		Purpose: "node",
		Listen:  carrierAddress,
		Enabled: true,
	}}
	return cfg
}

func vlessProfile(id string) configstore.XrayProfile {
	return configstore.XrayProfile{
		ID:   id,
		Kind: "vless",
		VLESS: &configstore.VLESSProfile{
			UUID:                   testVLESSUUID,
			Transport:              "tcp",
			Security:               "none",
			AllowInsecurePlaintext: true,
		},
	}
}

func singleCarrierAccount(t *testing.T, cfg configstore.Config) string {
	t.Helper()
	compiled, err := xrayconfig.Compile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(compiled.CarrierPeers) != 1 {
		t.Fatalf("carrier account count=%d, want 1", len(compiled.CarrierPeers))
	}
	for account := range compiled.CarrierPeers {
		return account
	}
	return ""
}

func reserveAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func startHalfCloseOrigin(t *testing.T) (string, <-chan [sha256.Size]byte) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	done := make(chan [sha256.Size]byte, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		payload, err := io.ReadAll(conn)
		if err != nil {
			return
		}
		digest := sha256.Sum256(payload)
		done <- digest
		_, _ = io.WriteString(conn, hex.EncodeToString(digest[:]))
	}()
	return listener.Addr().String(), done
}

func startObservedEchoOrigin(t *testing.T) (string, <-chan []byte) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	done := make(chan []byte, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- nil
			return
		}
		defer conn.Close()
		var received []byte
		buffer := make([]byte, 4096)
		for {
			count, readErr := conn.Read(buffer)
			if count > 0 {
				received = append(received, buffer[:count]...)
				if _, writeErr := conn.Write(buffer[:count]); writeErr != nil {
					done <- received
					return
				}
			}
			if readErr != nil {
				done <- received
				return
			}
		}
	}()
	return listener.Addr().String(), done
}

func startEchoOrigin(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return listener.Addr().String()
}

func assertSOCKSEcho(t *testing.T, socksAddress, origin string) {
	t.Helper()
	dialer, err := proxy.SOCKS5("tcp", socksAddress, &proxy.Auth{User: "terminal", Password: "entry-secret"}, proxy.Direct)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := dialer.Dial("tcp", origin)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	payload := []byte("previous runtime remains authoritative")
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("echo=%q, want %q", got, payload)
	}
}

func closePlane(t *testing.T, plane *Plane) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- plane.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Close: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Error("Close timed out")
	}
}

type fakeRendrRuntime struct {
	mu              sync.Mutex
	state           string
	setDialersCalls int
	dialStarted     chan struct{}
	dialStartOnce   sync.Once
	dialRelease     <-chan struct{}
	acceptCarrier   bool
	injected        chan net.Conn
	closeStarted    chan struct{}
	closeStartOnce  sync.Once
	closeRelease    <-chan struct{}
}

func newFakeRendrRuntime(state string) *fakeRendrRuntime {
	return &fakeRendrRuntime{state: state, closeStarted: make(chan struct{})}
}

func (r *fakeRendrRuntime) SetDialers(rendradapter.StreamDialer, rendradapter.EgressDialer) error {
	r.mu.Lock()
	r.setDialersCalls++
	r.mu.Unlock()
	return nil
}

func (r *fakeRendrRuntime) Dial(ctx context.Context, _ string, _ rendradapter.Destination) (net.Conn, error) {
	if r.dialStarted == nil {
		return nil, errors.New("fake rendr dial is unavailable")
	}
	r.dialStartOnce.Do(func() { close(r.dialStarted) })
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-r.dialRelease:
		return nil, errors.New("fake rendr dial released")
	}
}

func (r *fakeRendrRuntime) InjectCarrier(_ context.Context, conn net.Conn) error {
	if !r.acceptCarrier {
		return errors.New("fake rendr carrier injection is unavailable")
	}
	if r.injected != nil {
		select {
		case r.injected <- conn:
		default:
		}
	}
	return nil
}

func (r *fakeRendrRuntime) Status() rendradapter.RuntimeStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return rendradapter.RuntimeStatus{State: r.state, ObservedAt: time.Now().UTC()}
}

func (r *fakeRendrRuntime) CloseContext(ctx context.Context) error {
	r.closeStartOnce.Do(func() { close(r.closeStarted) })
	if r.closeRelease != nil {
		select {
		case <-r.closeRelease:
		case <-ctx.Done():
			return errors.Join(rendradapter.ErrShutdownIncomplete, ctx.Err())
		}
	}
	r.mu.Lock()
	r.state = "stopped"
	r.mu.Unlock()
	return nil
}

func (r *fakeRendrRuntime) ForceClose() error {
	if r.closeRelease != nil {
		return rendradapter.ErrShutdownIncomplete
	}
	r.mu.Lock()
	r.state = "stopped"
	r.mu.Unlock()
	return nil
}
