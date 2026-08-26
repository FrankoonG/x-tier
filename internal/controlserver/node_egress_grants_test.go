package controlserver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"sync"
	"testing"

	"github.com/FrankoonG/x-tier/internal/cli"
	"github.com/FrankoonG/x-tier/internal/configstore"
	"github.com/FrankoonG/x-tier/internal/controlapi"
	"github.com/FrankoonG/x-tier/internal/route"
	"github.com/FrankoonG/x-tier/internal/statestore"
)

const nodeEgressGrantTestLocalNodeID = "node-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestDomainNodeEgressGrantTypedLifecycleWithoutCLI(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := nodeEgressGrantDomainConfig(false)
	if err := configstore.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	server := startTestServer(t, path)
	forbidDomainCLIExecution(server)

	status, body := requestDomain(t, server, http.MethodGet, controlapi.DomainNodeEgressGrantsPath, nil)
	if status != http.StatusOK {
		t.Fatalf("initial GET status=%d body=%s", status, body)
	}
	var initial controlapi.NodeEgressGrantsResponse
	if err := json.Unmarshal(body, &initial); err != nil {
		t.Fatal(err)
	}
	if initial.APIVersion != controlapi.DomainAPIVersion || !initial.OK || initial.Revision != 0 ||
		initial.TargetLocalNodeID != nodeEgressGrantTestLocalNodeID || initial.NodeEgressGrants == nil ||
		len(initial.NodeEgressGrants) != 0 {
		t.Fatalf("initial typed response=%+v", initial)
	}
	assertNoCLIEnvelope(t, body)

	dryRun := nodeEgressGrantPutRequest(0, "61000000000000000000000000000000")
	dryRun.DryRun = true
	status, body = requestDomain(t, server, http.MethodPut, controlapi.DomainNodeEgressGrantsPath, dryRun)
	if status != http.StatusOK || !bytes.Contains(body, []byte(`"dry_run":true`)) ||
		!bytes.Contains(body, []byte(`"before_revision":0`)) || !bytes.Contains(body, []byte(`"after_revision":1`)) ||
		bytes.Contains(body, []byte(`"applied"`)) || bytes.Contains(body, []byte(`"outcome"`)) {
		t.Fatalf("PUT dry-run status=%d body=%s", status, body)
	}
	assertNodeEgressGrantResponseHasNoSecrets(t, body)
	assertNodeEgressGrantStoredState(t, path, 0, false)

	byName := nodeEgressGrantPutRequest(0, "6100000000000000000000000000000f")
	byName.SourceNodeID = "A"
	status, body = requestDomain(t, server, http.MethodPut, controlapi.DomainNodeEgressGrantsPath, byName)
	if status != http.StatusNotFound ||
		!bytes.Contains(body, []byte(`"error_code":"config.node_egress_grant_peer_unknown"`)) ||
		bytes.Contains(body, []byte(`"source_node_id":"A"`)) {
		t.Fatalf("peer-name PUT status=%d body=%s", status, body)
	}
	assertNodeEgressGrantStoredState(t, path, 0, false)

	put := nodeEgressGrantPutRequest(0, "61000000000000000000000000000001")
	status, body = requestDomain(t, server, http.MethodPut, controlapi.DomainNodeEgressGrantsPath, put)
	if status != http.StatusOK || !bytes.Contains(body, []byte(`"applied":true`)) ||
		!bytes.Contains(body, []byte(`"after_revision":1`)) ||
		!bytes.Contains(body, []byte(`"source_node_id":"node-a"`)) {
		t.Fatalf("PUT status=%d body=%s", status, body)
	}
	assertNodeEgressGrantResponseHasNoSecrets(t, body)
	assertNodeEgressGrantStoredState(t, path, 1, true)

	status, body = requestDomain(t, server, http.MethodGet, controlapi.DomainNodeEgressGrantsPath, nil)
	if status != http.StatusOK {
		t.Fatalf("populated GET status=%d body=%s", status, body)
	}
	var populated controlapi.NodeEgressGrantsResponse
	if err := json.Unmarshal(body, &populated); err != nil {
		t.Fatal(err)
	}
	grant, found := populated.NodeEgressGrants["node-a"]
	if !found || populated.Revision != 1 || grant.SourceNodeID != "node-a" || grant.Network != "tcp" ||
		len(grant.AllowCIDRs) != 1 || grant.AllowCIDRs[0] != "8.0.0.0/8" ||
		len(grant.AllowPrivateCIDRs) != 1 || grant.AllowPrivateCIDRs[0] != "10.20.0.0/16" ||
		len(grant.DenyCIDRs) != 1 || grant.DenyCIDRs[0] != "8.8.8.0/24" ||
		len(grant.AllowPorts) != 1 || grant.AllowPorts[0] != (controlapi.EgressPortRange{From: 443, To: 443}) {
		t.Fatalf("populated typed response=%+v", populated)
	}

	replacement := controlapi.NodeEgressGrantPutRequest{
		DomainMutationRequest: domainMutationRequest(1, "61000000000000000000000000000002"),
		SourceNodeID:          "node-a",
		Network:               "tcp",
		AllowCIDRs:            []string{"9.0.0.0/8"},
		AllowPorts:            []controlapi.EgressPortRange{{From: 8443, To: 8443}},
	}
	status, body = requestDomain(t, server, http.MethodPut, controlapi.DomainNodeEgressGrantsPath, replacement)
	if status != http.StatusOK || bytes.Contains(body, []byte("10.20.0.0/16")) || bytes.Contains(body, []byte("8.8.8.0/24")) {
		t.Fatalf("replacement PUT status=%d body=%s", status, body)
	}
	loaded, err := configstore.LoadExisting(path)
	if err != nil {
		t.Fatal(err)
	}
	replaced := loaded.NodeEgressGrants["node-a"]
	if loaded.Revision != 2 || len(replaced.AllowPrivateCIDRs) != 0 || len(replaced.DenyCIDRs) != 0 ||
		len(replaced.AllowCIDRs) != 1 || replaced.AllowCIDRs[0] != "9.0.0.0/8" ||
		len(replaced.AllowPorts) != 1 || replaced.AllowPorts[0] != (configstore.EgressPortRange{From: 8443, To: 8443}) {
		t.Fatalf("grant was not completely replaced: revision=%d grant=%+v", loaded.Revision, replaced)
	}

	missing := controlapi.NodeEgressGrantRevokeRequest{
		DomainMutationRequest: domainMutationRequest(2, "61000000000000000000000000000003"),
		SourceNodeID:          "private-missing-node",
	}
	status, body = requestDomain(t, server, http.MethodDelete, controlapi.DomainNodeEgressGrantsPath, missing)
	if status != http.StatusNotFound || !bytes.Contains(body, []byte(`"error_code":"config.node_egress_grant_unknown"`)) ||
		bytes.Contains(body, []byte(missing.SourceNodeID)) {
		t.Fatalf("missing DELETE status=%d body=%s", status, body)
	}
	assertNodeEgressGrantStoredState(t, path, 2, true)

	revoke := controlapi.NodeEgressGrantRevokeRequest{
		DomainMutationRequest: domainMutationRequest(2, "61000000000000000000000000000004"),
		SourceNodeID:          "node-a",
	}
	revoke.DryRun = true
	status, body = requestDomain(t, server, http.MethodDelete, controlapi.DomainNodeEgressGrantsPath, revoke)
	if status != http.StatusOK || !bytes.Contains(body, []byte(`"dry_run":true`)) ||
		!bytes.Contains(body, []byte(`"after_revision":3`)) || bytes.Contains(body, []byte(`"applied"`)) {
		t.Fatalf("DELETE dry-run status=%d body=%s", status, body)
	}
	assertNodeEgressGrantStoredState(t, path, 2, true)

	revoke.DryRun = false
	revoke.RequestID = "61000000000000000000000000000005"
	status, body = requestDomain(t, server, http.MethodDelete, controlapi.DomainNodeEgressGrantsPath, revoke)
	if status != http.StatusOK || !bytes.Contains(body, []byte(`"after_revision":3`)) ||
		!bytes.Contains(body, []byte(`"source_node_id":"node-a"`)) {
		t.Fatalf("DELETE status=%d body=%s", status, body)
	}
	assertNodeEgressGrantStoredState(t, path, 3, false)
	assertDomainNeverEnteredCLI(t, server)
}

func TestDomainNodeEgressGrantCASAllowsOneWinner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := configstore.Save(path, nodeEgressGrantDomainConfig(false)); err != nil {
		t.Fatal(err)
	}
	server := startTestServer(t, path)
	forbidDomainCLIExecution(server)

	requests := []controlapi.NodeEgressGrantPutRequest{
		nodeEgressGrantPutRequest(0, "62000000000000000000000000000000"),
		nodeEgressGrantPutRequest(0, "62000000000000000000000000000001"),
	}
	requests[1].AllowCIDRs = []string{"9.0.0.0/8"}

	type response struct {
		status int
		body   []byte
		err    error
	}
	start := make(chan struct{})
	responses := make(chan response, len(requests))
	var group sync.WaitGroup
	for _, request := range requests {
		request := request
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			status, body, err := requestDomainRaw(server, http.MethodPut, controlapi.DomainNodeEgressGrantsPath, request)
			responses <- response{status: status, body: body, err: err}
		}()
	}
	close(start)
	group.Wait()
	close(responses)

	var succeeded, conflicted int
	for response := range responses {
		if response.err != nil {
			t.Fatal(response.err)
		}
		switch response.status {
		case http.StatusOK:
			succeeded++
		case http.StatusConflict:
			if !bytes.Contains(response.body, []byte(`"error_code":"config.revision_conflict"`)) {
				t.Fatalf("CAS conflict body=%s", response.body)
			}
			conflicted++
		default:
			t.Fatalf("CAS status=%d body=%s", response.status, response.body)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("CAS results succeeded=%d conflicted=%d", succeeded, conflicted)
	}
	loaded, err := configstore.LoadExisting(path)
	if err != nil {
		t.Fatal(err)
	}
	grant := loaded.NodeEgressGrants["node-a"]
	if loaded.Revision != 1 || len(loaded.NodeEgressGrants) != 1 || len(grant.AllowCIDRs) != 1 ||
		(grant.AllowCIDRs[0] != "8.0.0.0/8" && grant.AllowCIDRs[0] != "9.0.0.0/8") {
		t.Fatalf("CAS final config revision=%d grant=%+v", loaded.Revision, grant)
	}
	assertDomainNeverEnteredCLI(t, server)
}

func TestDomainNodeEgressGrantUsesOwnedStoreCAS(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := nodeEgressGrantDomainConfig(false)
	if err := configstore.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	canonical, err := configstore.CanonicalPath(path)
	if err != nil {
		t.Fatal(err)
	}
	store, err := statestore.Open(canonical)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := configstore.SaveStore(store, cfg); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	server, err := StartOwnedStore(ctx, "127.0.0.1:0", store, canonical)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	forbidDomainCLIExecution(server)
	token, err := controlapi.ReadStoreToken(store, statestore.ControlToken)
	if err != nil {
		t.Fatal(err)
	}

	request := nodeEgressGrantPutRequest(0, "62500000000000000000000000000000")
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	status, response, err := controlapi.AuthenticatedRequestTokenContext(
		context.Background(), server.Addr(), token, http.MethodPut, controlapi.DomainNodeEgressGrantsPath, body,
	)
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusOK || !bytes.Contains(response, []byte(`"after_revision":1`)) {
		t.Fatalf("owned-store PUT status=%d body=%s", status, response)
	}
	loaded, err := configstore.LoadStoreExisting(store)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Revision != 1 || len(loaded.NodeEgressGrants) != 1 {
		t.Fatalf("owned-store config=%+v", loaded)
	}

	request.RequestID = "62500000000000000000000000000001"
	body, err = json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	status, response, err = controlapi.AuthenticatedRequestTokenContext(
		context.Background(), server.Addr(), token, http.MethodPut, controlapi.DomainNodeEgressGrantsPath, body,
	)
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusConflict || !bytes.Contains(response, []byte(`"error_code":"config.revision_conflict"`)) {
		t.Fatalf("owned-store stale PUT status=%d body=%s", status, response)
	}
	loaded, err = configstore.LoadStoreExisting(store)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Revision != 1 || len(loaded.NodeEgressGrants) != 1 {
		t.Fatalf("stale owned-store PUT changed config=%+v", loaded)
	}
	assertDomainNeverEnteredCLI(t, server)
}

func TestDomainPeerGrantLifecycleUsesConfigOps(t *testing.T) {
	t.Run("disable preserves grant and direction requires explicit revoke", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := configstore.Save(path, nodeEgressGrantDomainConfig(true)); err != nil {
			t.Fatal(err)
		}
		server := startTestServer(t, path)
		forbidDomainCLIExecution(server)

		status, body := requestDomain(t, server, http.MethodPatch, controlapi.DomainPeerStatePath, controlapi.PeerStateRequest{
			DomainMutationRequest: domainMutationRequest(0, "63000000000000000000000000000000"),
			Name:                  "A",
			Enabled:               false,
			Reason:                "maintenance",
		})
		if status != http.StatusOK {
			t.Fatalf("disable status=%d body=%s", status, body)
		}
		assertNodeEgressGrantStoredState(t, path, 1, true)

		outbound := string(route.DirectionOutbound)
		status, body = requestDomain(t, server, http.MethodPatch, controlapi.DomainPeersPath, controlapi.PeerUpdateRequest{
			DomainMutationRequest: domainMutationRequest(1, "63000000000000000000000000000001"),
			Name:                  "A",
			Patch:                 controlapi.PeerPatch{Direction: &outbound},
		})
		if status != http.StatusConflict ||
			!bytes.Contains(body, []byte(`"error_code":"config.node_egress_grant_revoke_required"`)) {
			t.Fatalf("direction without revoke status=%d body=%s", status, body)
		}
		loaded, err := configstore.LoadExisting(path)
		if err != nil {
			t.Fatal(err)
		}
		if loaded.Revision != 1 || loaded.Peers[0].Direction != route.DirectionInbound || len(loaded.NodeEgressGrants) != 1 {
			t.Fatalf("rejected direction partially mutated config: %+v", loaded)
		}

		status, body = requestDomain(t, server, http.MethodPatch, controlapi.DomainPeersPath, controlapi.PeerUpdateRequest{
			DomainMutationRequest: domainMutationRequest(1, "63000000000000000000000000000002"),
			Name:                  "A",
			Patch: controlapi.PeerPatch{
				Direction:             &outbound,
				RevokeNodeEgressGrant: true,
			},
		})
		if status != http.StatusOK || !bytes.Contains(body, []byte(`"node_egress_grant_revoked":true`)) {
			t.Fatalf("direction with revoke status=%d body=%s", status, body)
		}
		loaded, err = configstore.LoadExisting(path)
		if err != nil {
			t.Fatal(err)
		}
		if loaded.Revision != 2 || loaded.Peers[0].Direction != route.DirectionOutbound || len(loaded.NodeEgressGrants) != 0 {
			t.Fatalf("direction and grant were not atomically changed: %+v", loaded)
		}
		assertDomainNeverEnteredCLI(t, server)
	})

	t.Run("remove cascades grant and reports canonical identity", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := configstore.Save(path, nodeEgressGrantDomainConfig(true)); err != nil {
			t.Fatal(err)
		}
		server := startTestServer(t, path)
		forbidDomainCLIExecution(server)

		status, body := requestDomain(t, server, http.MethodDelete, controlapi.DomainPeersPath, controlapi.PeerRemoveRequest{
			DomainMutationRequest: domainMutationRequest(0, "63000000000000000000000000000003"),
			Name:                  "A",
		})
		if status != http.StatusOK || !bytes.Contains(body, []byte(`"node_id":"node-a"`)) ||
			!bytes.Contains(body, []byte(`"node_egress_grant_revoked":true`)) {
			t.Fatalf("peer remove status=%d body=%s", status, body)
		}
		loaded, err := configstore.LoadExisting(path)
		if err != nil {
			t.Fatal(err)
		}
		if loaded.Revision != 1 || len(loaded.Peers) != 0 || len(loaded.NodeEgressGrants) != 0 {
			t.Fatalf("peer and grant were not atomically removed: %+v", loaded)
		}
		assertDomainNeverEnteredCLI(t, server)
	})
}

func nodeEgressGrantDomainConfig(withGrant bool) configstore.Config {
	cfg := configstore.DefaultConfig()
	cfg.Node.NodeID = nodeEgressGrantTestLocalNodeID
	cfg.XrayProfiles["peer-vless"] = configstore.XrayProfile{
		ID:   "peer-vless",
		Kind: "vless",
		VLESS: &configstore.VLESSProfile{
			UUID:                   "11111111-1111-4111-8111-111111111111",
			Transport:              "tcp",
			Security:               "none",
			AllowInsecurePlaintext: true,
		},
	}
	cfg.Peers = []configstore.PeerConfig{{
		Name:          "A",
		NodeID:        "node-a",
		DisplayName:   "A",
		Addr:          "127.0.0.1:24443",
		GatewayAddr:   "127.0.0.1:24443",
		Direction:     route.DirectionInbound,
		XrayProfileID: "peer-vless",
		Enabled:       true,
		RendrCapable:  true,
	}}
	if withGrant {
		cfg.NodeEgressGrants["node-a"] = nodeEgressGrantConfigValue()
	}
	return cfg
}

func nodeEgressGrantPutRequest(revision int64, requestID string) controlapi.NodeEgressGrantPutRequest {
	return controlapi.NodeEgressGrantPutRequest{
		DomainMutationRequest: domainMutationRequest(revision, requestID),
		SourceNodeID:          "node-a",
		Network:               "tcp",
		AllowCIDRs:            []string{"8.0.0.0/8"},
		AllowPrivateCIDRs:     []string{"10.20.0.0/16"},
		DenyCIDRs:             []string{"8.8.8.0/24"},
		AllowPorts:            []controlapi.EgressPortRange{{From: 443, To: 443}},
	}
}

func nodeEgressGrantConfigValue() configstore.NodeEgressGrant {
	return configstore.NodeEgressGrant{
		SourceNodeID:      "node-a",
		Network:           "tcp",
		AllowCIDRs:        []string{"8.0.0.0/8"},
		AllowPrivateCIDRs: []string{"10.20.0.0/16"},
		DenyCIDRs:         []string{"8.8.8.0/24"},
		AllowPorts:        []configstore.EgressPortRange{{From: 443, To: 443}},
	}
}

func assertNodeEgressGrantStoredState(t *testing.T, path string, revision int64, present bool) {
	t.Helper()
	cfg, err := configstore.LoadExisting(path)
	if err != nil {
		t.Fatal(err)
	}
	_, found := cfg.NodeEgressGrants["node-a"]
	if cfg.Revision != revision || found != present {
		t.Fatalf("stored revision=%d grant_present=%t, want revision=%d grant_present=%t", cfg.Revision, found, revision, present)
	}
}

func assertNodeEgressGrantResponseHasNoSecrets(t *testing.T, body []byte) {
	t.Helper()
	for _, forbidden := range []string{"credential", "password", "private_key", "seed", "token", "secret"} {
		if bytes.Contains(bytes.ToLower(body), []byte(forbidden)) {
			t.Fatalf("node egress grant response exposed secret-bearing field %q: %s", forbidden, body)
		}
	}
	assertNoCLIEnvelope(t, body)
}

func forbidDomainCLIExecution(server *Server) {
	server.execute = func(context.Context, []string, io.Writer, io.Writer) cli.ExecutionResult {
		panic("typed domain operation invoked CLI")
	}
}

func assertDomainNeverEnteredCLI(t *testing.T, server *Server) {
	t.Helper()
	if server.commandIngress.Load() != 0 || server.commandExecutions.Load() != 0 {
		t.Fatalf("domain operations entered CLI: ingress=%d executions=%d", server.commandIngress.Load(), server.commandExecutions.Load())
	}
}

func TestDomainNodeEgressGrantErrorStatus(t *testing.T) {
	if got := domainErrorStatus("config.node_egress_grant_revoke_required"); got != http.StatusConflict {
		t.Fatalf("revoke-required status=%d, want %d", got, http.StatusConflict)
	}
	if got := domainErrorStatus("config.node_egress_grant_unknown"); got != http.StatusNotFound {
		t.Fatalf("unknown grant status=%d, want %d", got, http.StatusNotFound)
	}
}

func TestDomainNodeEgressGrantRequestRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := configstore.Save(path, nodeEgressGrantDomainConfig(false)); err != nil {
		t.Fatal(err)
	}
	server := startTestServer(t, path)
	forbidDomainCLIExecution(server)
	payload := []byte(`{"api_version":1,"revision":0,"dry_run":false,"request_id":"64000000000000000000000000000000","source_node_id":"node-a","network":"tcp","allow_cidrs":["8.0.0.0/8"],"allow_private_cidrs":[],"deny_cidrs":[],"allow_ports":[{"from":443,"to":443}],"credential":"must-not-be-read"}`)
	status, body, err := requestDomainBytes(server, http.MethodPut, controlapi.DomainNodeEgressGrantsPath, payload)
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusBadRequest || !bytes.Contains(body, []byte(`"error_code":"domain.request_invalid"`)) ||
		bytes.Contains(body, []byte("must-not-be-read")) {
		t.Fatalf("unknown-field status=%d body=%s", status, body)
	}
	assertNodeEgressGrantStoredState(t, path, 0, false)
	assertDomainNeverEnteredCLI(t, server)
}
