// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// initRecorder is a node's tenant service that reports nodes serving the
// barrier and records every passphrase init it is sent, copying the
// passphrase the CLI wipes on return. The nodes of a fan-out are sent their
// init together, so one recorder shared by several nodes guards its record.
type initRecorder struct {
	svc      *mockTenantService
	mu       sync.Mutex
	inits    [][]byte
	unseals  [][]byte
	statuses int
}

// newUnsealRecorder is newInitRecorder's node that also records each
// barrier unseal's key, refusing it with refuse.
func newUnsealRecorder(nodes []tenant.BarrierNode, refuse error) *initRecorder {
	r := newInitRecorder(nodes, nil)
	r.svc.barrierUnsealFn = func(_ context.Context, _ string, key []byte) error {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.unseals = append(r.unseals, append([]byte(nil), key...))
		return refuse
	}
	return r
}

func newInitRecorder(nodes []tenant.BarrierNode, refuse error) *initRecorder {
	r := &initRecorder{}
	r.svc = &mockTenantService{
		barrierStatusFn: func(_ context.Context, id string) (*tenant.TenantBarrierStatus, error) {
			r.statuses++
			return &tenant.TenantBarrierStatus{TenantID: id, Sealed: true, Nodes: nodes}, nil
		},
		barrierInitFn: func(_ context.Context, _ string, passphrase []byte, _, _ int) error {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.inits = append(r.inits, append([]byte(nil), passphrase...))
			return refuse
		},
	}
	return r
}

// tenantRootOver builds the real root command over an embedded node backed by
// svc.
func tenantRootOver(svc *mockTenantService) func() *cobra.Command {
	return func() *cobra.Command {
		return newTestRootCmdWithOpts(newMockKVService(), newMockAdminService(), embedded.WithTenantService(svc))
	}
}

// servingNodeOn is a node backed by its own embedded service.
func servingNodeOn(t *testing.T, addr string, svc *mockTenantService) servingNode {
	t.Helper()
	tr, err := embedded.New(&noopKVService{}, newMockAdminService(), embedded.WithTenantService(svc))
	if err != nil {
		t.Fatalf("embedded.New: %v", err)
	}
	cl, err := sdk.NewClient(tr)
	if err != nil {
		t.Fatalf("sdk.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = cl.Close() })
	return servingNode{addr: addr, client: cl}
}

// passphraseInit is the op barrier-init sends each node.
func passphraseInit(pass string) func(context.Context, transport.TenantClient) error {
	req := &transport.TenantBarrierInitRequest{TenantID: "acme", Passphrase: []byte(pass)}
	return func(ctx context.Context, tenants transport.TenantClient) error {
		_, err := tenants.TenantBarrierInit(ctx, req)
		return err
	}
}

// TestSendToServingNodes_SendsToEveryNode proves one init reaches every node,
// and that a node that refuses it or could not be reached does not stop the
// others.
func TestSendToServingNodes_SendsToEveryNode(t *testing.T) {
	n1, n3 := newInitRecorder(nil, nil), newInitRecorder(nil, nil)
	n4 := newInitRecorder(nil, errors.New("already initialized"))
	nodes := []servingNode{
		servingNodeOn(t, "n1", n1.svc),
		{addr: "n2", err: &NodeEndpointMissingError{Node: "n2", Transport: "grpc"}},
		servingNodeOn(t, "n3", n3.svc),
		servingNodeOn(t, "n4", n4.svc),
	}
	results, err := sendToServingNodes(context.Background(), nodes, "the barrier init", passphraseInit("pass"))
	var fanOut *BarrierFanOutError
	if !errors.As(err, &fanOut) || strings.Join(fanOut.Failed, ",") != "n2,n4" {
		t.Fatalf("err = %v; want *BarrierFanOutError naming n2 and n4", err)
	}
	if !strings.Contains(fanOut.Error(), "the barrier init was refused by n2, n4") {
		t.Fatalf("Error() = %q", fanOut.Error())
	}
	if len(n1.inits) != 1 || len(n3.inits) != 1 || string(n1.inits[0]) != "pass" || string(n3.inits[0]) != "pass" {
		t.Fatal("a node that answered was not sent the passphrase")
	}
	if len(results) != 4 || results[0].Error != "" || !strings.Contains(results[1].Error, "publishes no grpc endpoint") ||
		results[2].Error != "" || !strings.Contains(results[3].Error, "already initialized") {
		t.Fatalf("results = %+v; want n2 and n4 refused", results)
	}
}

// TestSendToServingNodes_AllAccept proves no error is returned when every
// node accepts, and each is sent the same passphrase.
func TestSendToServingNodes_AllAccept(t *testing.T) {
	r := newInitRecorder(nil, nil)
	nodes := []servingNode{servingNodeOn(t, "n1", r.svc), servingNodeOn(t, "n2", r.svc)}
	results, err := sendToServingNodes(context.Background(), nodes, "the barrier init", passphraseInit("pass"))
	if err != nil || len(results) != 2 || len(r.inits) != 2 || string(r.inits[1]) != "pass" {
		t.Fatalf("results = %+v, err = %v, inits = %q; want both nodes initialized", results, err, r.inits)
	}
}

// TestSendToServingNodes_SendsToEveryNodeAtOnce proves the nodes are sent op
// together: an init that waits for its replica group to form returns only once
// enough of the group's other nodes have initialized, so a node-at-a-time
// fan-out holds each call for its group's whole startup budget.
func TestSendToServingNodes_SendsToEveryNodeAtOnce(t *testing.T) {
	const count = 3
	var arrived atomic.Int32
	all := make(chan struct{})
	svc := &mockTenantService{
		barrierInitFn: func(ctx context.Context, _ string, _ []byte, _, _ int) error {
			if arrived.Add(1) == count {
				close(all)
			}
			select {
			case <-all:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	}
	nodes := make([]servingNode, 0, count)
	for _, addr := range []string{"n1", "n2", "n3"} {
		nodes = append(nodes, servingNodeOn(t, addr, svc))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	results, err := sendToServingNodes(ctx, nodes, "the barrier init", passphraseInit("pass"))
	if err != nil {
		t.Fatalf("err = %v, results = %+v; want every node initialized together", err, results)
	}
	for i, addr := range []string{"n1", "n2", "n3"} {
		if results[i].Node != addr || results[i].Error != "" {
			t.Fatalf("results[%d] = %+v; want %s accepted, in the order the nodes were listed", i, results[i], addr)
		}
	}
}

// TestRenderNodeResults_PrintsEveryNode proves each node's answer is printed,
// in the table and as JSON.
func TestRenderNodeResults_PrintsEveryNode(t *testing.T) {
	results := []nodeResult{{Node: "n1"}, {Node: "n2", Error: "refused here"}}
	for format, check := range map[string]func(string) bool{
		"table": func(out string) bool {
			return strings.Contains(out, "n1: OK") && strings.Contains(out, "n2: refused: refused here")
		},
		"json": func(out string) bool {
			var got []nodeResult
			return json.Unmarshal([]byte(out), &got) == nil && len(got) == 2 && got[0].Error == "" && got[1].Error == "refused here"
		},
	} {
		t.Run(format, func(t *testing.T) {
			var buf bytes.Buffer
			savedWriter, savedFormat := outputWriter, outputFormat
			defer func() { outputWriter, outputFormat = savedWriter, savedFormat }()
			outputWriter, outputFormat = &buf, format
			if err := renderNodeResults(results); err != nil {
				t.Fatal(err)
			}
			if !check(buf.String()) {
				t.Fatalf("output = %q", buf.String())
			}
		})
	}
}

// TestTenantBarrierInit_FansOutToServingNodes proves a passphrase init on a
// barrier served by several nodes is sent to each over --server's transport,
// at the endpoint each publishes for it, and not to --server's node in their
// stead: a node publishing no endpoint is reported, and one that cannot be
// reached is reported without stopping the others.
func TestTenantBarrierInit_FansOutToServingNodes(t *testing.T) {
	r := newInitRecorder([]tenant.BarrierNode{
		{Node: "n1:7000", Endpoints: map[string]string{"grpc": "n1:9443"}},
		{Node: "n2:7000", Endpoints: map[string]string{"rest": "127.0.0.1:1"}},
	}, nil)
	out, err := runCLI(tenantRootOver(r.svc)(), context.Background(), "--transport", "rest", "--timeout", "2s",
		"tenant", "barrier-init", "--id", "acme", "--passphrase", "pass")
	var fanOut *BarrierFanOutError
	if !errors.As(err, &fanOut) || fanOut.Op != "the barrier init" || strings.Join(fanOut.Failed, ",") != "n1:7000,n2:7000" {
		t.Fatalf("err = %v; want *BarrierFanOutError naming n1 and n2", err)
	}
	if !strings.Contains(out, "n1:7000: refused: qrdb: node n1:7000 publishes no rest endpoint") ||
		!strings.Contains(out, "n2:7000: refused:") {
		t.Fatalf("output = %q; want both nodes reported", out)
	}
	if r.statuses != 1 || len(r.inits) != 0 {
		t.Fatalf("%d statuses, %d inits at --server's node; want the status read and no init", r.statuses, len(r.inits))
	}
}

func TestTenantBarrierUnseal_FansOutToServingNodes(t *testing.T) {
	n1, n3 := newUnsealRecorder(nil, nil), newUnsealRecorder(nil, nil)
	n2 := newUnsealRecorder(nil, errors.New("invalid key"))
	nodes := []servingNode{servingNodeOn(t, "n1", n1.svc), servingNodeOn(t, "n2", n2.svc), servingNodeOn(t, "n3", n3.svc)}
	req := &transport.TenantBarrierUnsealRequest{TenantID: "acme", Key: []byte("credential")}
	results, err := sendToServingNodes(context.Background(), nodes, "the barrier unseal", barrierUnseal(req))
	var fanOut *BarrierFanOutError
	if !errors.As(err, &fanOut) || strings.Join(fanOut.Failed, ",") != "n2" ||
		!strings.Contains(fanOut.Error(), "the barrier unseal was refused by n2") {
		t.Fatalf("err = %v; want *BarrierFanOutError naming n2", err)
	}
	for name, r := range map[string]*initRecorder{"n1": n1, "n2": n2, "n3": n3} {
		if len(r.unseals) != 1 || string(r.unseals[0]) != "credential" {
			t.Fatalf("%s received unseals %q; want the credential once", name, r.unseals)
		}
	}
	if len(results) != 3 || results[0].Error != "" || !strings.Contains(results[1].Error, "invalid key") || results[2].Error != "" {
		t.Fatalf("results = %+v; want n2 refused, n1 and n3 unsealed", results)
	}
}

func TestTenantBarrierUnseal_SeveralNodesFanOut(t *testing.T) {
	key := base64.StdEncoding.EncodeToString([]byte("credential"))
	for _, transportType := range []string{transportREST, transportGRPC, transportQUIC, transportMCP} {
		t.Run(transportType, func(t *testing.T) {
			r := newUnsealRecorder([]tenant.BarrierNode{unreachableOnEvery("n1:7000"), unreachableOnEvery("n2:7000"), unreachableOnEvery("n3:7000")}, nil)
			out, err := runCLI(tenantRootOver(r.svc)(), context.Background(), "--transport", transportType, "--timeout", "2s",
				"tenant", "barrier-unseal", "--id", "acme", "--key", key)
			var fanOut *BarrierFanOutError
			if !errors.As(err, &fanOut) || fanOut.Op != "the barrier unseal" || strings.Join(fanOut.Failed, ",") != "n1:7000,n2:7000,n3:7000" {
				t.Fatalf("err = %v; want *BarrierFanOutError naming n1, n2 and n3", err)
			}
			for _, n := range []string{"n1:7000: refused:", "n2:7000: refused:", "n3:7000: refused:"} {
				if !strings.Contains(out, n) {
					t.Fatalf("output = %q; want every node reported", out)
				}
			}
			if r.statuses != 1 || len(r.unseals) != 0 {
				t.Fatalf("%d statuses, %d unseals at --server's node; want the status read and no unseal", r.statuses, len(r.unseals))
			}
		})
	}
}

func TestTenantBarrierUnseal_OneNodeUnsealsServerNode(t *testing.T) {
	r := newUnsealRecorder([]tenant.BarrierNode{unreachableOnEvery("n1:7000")}, nil)
	out, err := runCLI(tenantRootOver(r.svc)(), context.Background(),
		"tenant", "barrier-unseal", "--id", "acme", "--key", base64.StdEncoding.EncodeToString([]byte("credential")))
	if err != nil {
		t.Fatalf("barrier-unseal: %v", err)
	}
	if r.statuses != 1 || len(r.unseals) != 1 || string(r.unseals[0]) != "credential" || !strings.Contains(out, "OK") {
		t.Fatalf("%d statuses, unseals %q, output %q; want --server's node unsealed", r.statuses, r.unseals, out)
	}
}

// TestTenantBarrierUnseal_UnreadableNodesRefuses pins that a status whose
// serving set could not be read makes barrier-unseal and barrier-init fail
// without sending to any node: an empty, unreadable list is not a
// standalone barrier.
func TestTenantBarrierUnseal_UnreadableNodesRefuses(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"unseal", []string{"tenant", "barrier-unseal", "--id", "acme", "--key", base64.StdEncoding.EncodeToString([]byte("credential"))}},
		{"init", []string{"tenant", "barrier-init", "--id", "acme", "--passphrase", "pass"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newUnsealRecorder(nil, nil)
			r.svc.barrierStatusFn = func(_ context.Context, id string) (*tenant.TenantBarrierStatus, error) {
				r.statuses++
				return &tenant.TenantBarrierStatus{TenantID: id, Sealed: true, NodesUnreadable: true}, nil
			}
			_, err := runCLI(tenantRootOver(r.svc)(), context.Background(), tc.args...)
			var unknown *TenantServingNodesUnknownError
			if !errors.As(err, &unknown) || unknown.TenantID != "acme" {
				t.Fatalf("err = %v, want TenantServingNodesUnknownError for acme", err)
			}
			if r.statuses != 1 || len(r.unseals) != 0 || len(r.inits) != 0 {
				t.Fatalf("%d statuses, unseals %q, inits %q; want nothing sent", r.statuses, r.unseals, r.inits)
			}
		})
	}
}

// TestTenantBarrierInit_FanOutNeedsANetworkTransport proves a barrier served
// by several nodes is not initialized over a transport that reaches only the
// node it runs beside.
func TestTenantBarrierInit_FanOutNeedsANetworkTransport(t *testing.T) {
	r := newInitRecorder([]tenant.BarrierNode{{Node: "n1:7000"}, {Node: "n2:7000"}}, nil)
	_, err := runCLI(tenantRootOver(r.svc)(), context.Background(),
		"tenant", "barrier-init", "--id", "acme", "--passphrase", "pass")
	var invalid *InvalidArgError
	if !errors.As(err, &invalid) || invalid.Arg != "--transport" || !strings.Contains(invalid.Reason, "not unix") {
		t.Fatalf("err = %v; want *InvalidArgError on --transport naming unix", err)
	}
	if len(r.inits) != 0 {
		t.Fatalf("%d inits; want none", len(r.inits))
	}
}

// TestTenantBarrierInit_OneNodeInitsServerNode proves a barrier the status
// lists no serving nodes for -- a standalone node's -- is initialized on
// --server's node, as before there were several.
func TestTenantBarrierInit_OneNodeInitsServerNode(t *testing.T) {
	r := newInitRecorder(nil, nil)
	out, err := runCLI(tenantRootOver(r.svc)(), context.Background(),
		"tenant", "barrier-init", "--id", "acme", "--passphrase", "pass")
	if err != nil {
		t.Fatalf("barrier-init: %v", err)
	}
	if r.statuses != 1 || len(r.inits) != 1 || string(r.inits[0]) != "pass" || !strings.Contains(out, "OK") {
		t.Fatalf("%d statuses, inits %q, output %q; want --server's node initialized", r.statuses, r.inits, out)
	}
}

// unreachableOnEvery is a serving node publishing, for every network
// transport, an endpoint nothing listens on: an init sent to it fails.
func unreachableOnEvery(node string) tenant.BarrierNode {
	endpoints := make(map[string]string, len(servingAddresses))
	for transportType := range servingAddresses {
		endpoints[transportType] = "127.0.0.1:1"
	}
	return tenant.BarrierNode{Node: node, Endpoints: endpoints}
}

// TestTenantBarrierInit_SingleMemberRaftInitsServerNode proves a passphrase
// init on a barrier one node serves -- a single-member Raft deployment's -- is
// sent on --server's client over every transport, unix included, and not
// dialled anew at the endpoint the node publishes: the endpoint here is
// unreachable, so a fan-out would fail.
func TestTenantBarrierInit_SingleMemberRaftInitsServerNode(t *testing.T) {
	for _, transportType := range []string{transportUnix, transportREST, transportGRPC, transportQUIC, transportMCP} {
		t.Run(transportType, func(t *testing.T) {
			r := newInitRecorder([]tenant.BarrierNode{unreachableOnEvery("n1:7000")}, nil)
			out, err := runCLI(tenantRootOver(r.svc)(), context.Background(), "--transport", transportType, "--timeout", "2s",
				"tenant", "barrier-init", "--id", "acme", "--passphrase", "pass")
			if err != nil {
				t.Fatalf("barrier-init over %s: %v\noutput: %s", transportType, err, out)
			}
			if r.statuses != 1 || len(r.inits) != 1 || string(r.inits[0]) != "pass" {
				t.Fatalf("%d statuses, inits %q; want the status read and --server's node initialized once", r.statuses, r.inits)
			}
			if strings.Contains(out, "n1:7000") {
				t.Fatalf("output = %q; a direct init prints no per-node result", out)
			}
		})
	}
}

// TestTenantBarrierInit_SeveralNodesStillFanOut proves the one-node direct
// path stops at one: two serving nodes are each sent the init at the endpoint
// they publish, over every network transport, and --server's node is not
// initialized in their stead.
func TestTenantBarrierInit_SeveralNodesStillFanOut(t *testing.T) {
	for _, transportType := range []string{transportREST, transportGRPC, transportQUIC, transportMCP} {
		t.Run(transportType, func(t *testing.T) {
			r := newInitRecorder([]tenant.BarrierNode{unreachableOnEvery("n1:7000"), unreachableOnEvery("n2:7000")}, nil)
			out, err := runCLI(tenantRootOver(r.svc)(), context.Background(), "--transport", transportType, "--timeout", "2s",
				"tenant", "barrier-init", "--id", "acme", "--passphrase", "pass")
			var fanOut *BarrierFanOutError
			if !errors.As(err, &fanOut) || strings.Join(fanOut.Failed, ",") != "n1:7000,n2:7000" {
				t.Fatalf("err = %v; want *BarrierFanOutError naming n1 and n2", err)
			}
			if !strings.Contains(out, "n1:7000: refused:") || !strings.Contains(out, "n2:7000: refused:") {
				t.Fatalf("output = %q; want both nodes reported", out)
			}
			if r.statuses != 1 || len(r.inits) != 0 {
				t.Fatalf("%d statuses, %d inits at --server's node; want the status read and no init", r.statuses, len(r.inits))
			}
		})
	}
}

// TestTenantBarrierInit_OtherStrategiesDoNotFanOut proves an init needing no
// passphrase goes to --server's node alone, without reading the serving
// nodes: the others initialize themselves from its claimed statement.
func TestTenantBarrierInit_OtherStrategiesDoNotFanOut(t *testing.T) {
	r := newInitRecorder([]tenant.BarrierNode{{Node: "n1:7000"}, {Node: "n2:7000"}}, nil)
	_, _ = runCLI(tenantRootOver(r.svc)(), context.Background(),
		"tenant", "barrier-init", "--id", "acme", "--strategy", "none")
	if r.statuses != 0 {
		t.Fatalf("%d statuses read; want none for a none-strategy init", r.statuses)
	}
}

func TestInitFansOut(t *testing.T) {
	for strategy, want := range map[string]bool{"": true, "passphrase": true, "none": false, "shamir": false, "tpm2": false} {
		if got := initFansOut(strategy); got != want {
			t.Errorf("initFansOut(%q) = %t, want %t", strategy, got, want)
		}
	}
}

// TestServerTransport proves the fan-out reaches nodes over the transport the
// root command resolves for --server: a scheme overrides a default
// --transport, and an explicit one wins.
func TestServerTransport(t *testing.T) {
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"default":                   {nil, transportUnix},
		"scheme over the default":   {[]string{"--server", "https://db.example.com:8443"}, transportREST},
		"explicit over the scheme":  {[]string{"--server", "https://db.example.com:8443", "--transport", "grpc"}, transportGRPC},
		"explicit with a bare host": {[]string{"--server", "db.example.com:9443", "--transport", "quic"}, transportQUIC},
	} {
		t.Run(name, func(t *testing.T) {
			root := newRootCmd()
			if err := root.PersistentFlags().Parse(tc.args); err != nil {
				t.Fatal(err)
			}
			if got := serverTransport(root); got != tc.want {
				t.Fatalf("serverTransport = %q, want %q", got, tc.want)
			}
		})
	}
}
