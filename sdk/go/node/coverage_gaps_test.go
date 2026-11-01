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

package node

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/firstboot"
	"github.com/jeremyhahn/go-qrdb/pkg/keydelivery"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/volume"
)

// newBareStandaloneNode starts an in-memory standalone node with every
// network protocol off.
func newBareStandaloneNode(t *testing.T) *Node {
	t.Helper()
	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = t.TempDir()
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendMemory)}}
	cfg.Protocols.REST, cfg.Protocols.GRPC, cfg.Protocols.QUIC = false, false, false
	cfg.Protocols.Unix, cfg.Protocols.MCP = false, false
	n, err := NewStandaloneNode(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("NewStandaloneNode: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })
	return n
}

// refusingVolumeKeyProvider is a volume.KeyProvider that never hands out a key.
type refusingVolumeKeyProvider struct{}

var errVolumeKeyRefused = errors.New("volume key refused")

func (refusingVolumeKeyProvider) VolumeKey(context.Context, string, string, string) (volume.VolumeKey, error) {
	return volume.VolumeKey{}, errVolumeKeyRefused
}

var _ volume.KeyProvider = refusingVolumeKeyProvider{}

// stubVolume is a volume.Volume whose Usage and Close fail on demand.
type stubVolume struct {
	root     string
	usageErr error
	closeErr error
	health   volume.Health
	closed   bool
}

func (v *stubVolume) Root() string { return v.root }

func (v *stubVolume) Usage(context.Context) (volume.Usage, error) {
	return volume.Usage{}, v.usageErr
}

func (v *stubVolume) Health(context.Context) volume.Health { return v.health }

func (v *stubVolume) Close(context.Context) error {
	if v.closed {
		return nil
	}
	v.closed = true
	return v.closeErr
}

var _ volume.Volume = (*stubVolume)(nil)

// stubDecrypter is a keydelivery.Decrypter that is never asked to decrypt.
type stubDecrypter struct{ keydelivery.Decrypter }

// The embedded caller authenticates through the live server, which refuses
// an empty chain; with no server it refuses as recycling.
func TestEmbeddedCaller_Authenticate(t *testing.T) {
	if id, err := (&embeddedCaller{node: &Node{}}).Authenticate(context.Background()); id != nil || !errors.As(err, new(*service.DataPlaneRecyclingError)) {
		t.Fatalf("Authenticate with no server = %v, %v; want *service.DataPlaneRecyclingError", id, err)
	}
	c := &embeddedCaller{node: newBareStandaloneNode(t)}
	if c.Name() != "embedded-caller" {
		t.Errorf("Name = %q", c.Name())
	}
	id, err := c.Authenticate(context.Background())
	if id != nil || !(errors.As(err, new(*server.CallerAuthUnavailableError)) || errors.As(err, new(*server.CallerCertificateError))) {
		t.Fatalf("Authenticate with no chain = %v, %v (%T); want the server's typed refusal", id, err, err)
	}
}

// The volume-pool reporter is not started without a registry, fails to
// construct with no node ID, and fails to start on a sealed node; each
// failure is logged and leaves the node without a reporter.
func TestStartVolumeReporter_Refusals(t *testing.T) {
	ctx := context.Background()
	svcNode := newBareStandaloneNode(t).svcNode.Load()
	registry, err := newVolumeRegistry()
	if err != nil {
		t.Fatal(err)
	}

	(&Node{}).startVolumeReporter(ctx, svcNode, "node-1")

	for name, tc := range map[string]struct {
		nodeID string
		sealed bool
		want   string
	}{
		"no node id":  {nodeID: "", want: "failed to construct volume-pool Reporter"},
		"sealed node": {nodeID: "node-1", sealed: true, want: "failed to start volume-pool Reporter"},
	} {
		t.Run(name, func(t *testing.T) {
			var logs bytes.Buffer
			n := &Node{volumeRegistry: registry, logger: slog.New(slog.NewTextHandler(&logs, nil))}
			if tc.sealed {
				barrier, cleanup := newTestBarrier(t)
				t.Cleanup(cleanup)
				n.barrier.Store(barrier)
			}
			n.startVolumeReporter(ctx, svcNode, tc.nodeID)
			if n.volumeReporter != nil {
				t.Fatal("a refused reporter was recorded on the node")
			}
			if !strings.Contains(logs.String(), tc.want) {
				t.Fatalf("logs = %q; want %q", logs.String(), tc.want)
			}
		})
	}
}

// A node with no barrier registry, volume registry or host attaches no
// catalog and reports no error.
func TestAttachVolumeCatalog_NothingToAttach(t *testing.T) {
	if err := (&Node{}).attachVolumeCatalog(nil); err != nil {
		t.Fatalf("attachVolumeCatalog = %v", err)
	}
}

// With no live server the node's registry refuses a stream as recycling.
func TestNodeServiceRegistry_StreamRefusalWithNoServer(t *testing.T) {
	r := &nodeServiceRegistry{node: &Node{}}
	if err := r.StreamRefusal("acme", "default"); !errors.As(err, new(*service.DataPlaneRecyclingError)) {
		t.Fatalf("StreamRefusal = %v; want *service.DataPlaneRecyclingError", err)
	}
}

// An embedded client over a node with no KV service is the typed
// ClientCreateError, and no client is returned.
func TestNewConnectedEmbeddedClient_RefusesAnEmptyNode(t *testing.T) {
	var create *ClientCreateError
	if c, err := newConnectedEmbeddedClient(context.Background(), &lifecycle.Node{}, nil); c != nil || !errors.As(err, &create) {
		t.Fatalf("client over an empty node = %v, %v; want *ClientCreateError", c, err)
	}
}

// TestNodeFirstBoot_StartedNodeReconciles runs Node.FirstBoot on a started
// node, so the success path reaches reconcileAwaitingAdminCredential. Under
// "enrolled" the node never enters "awaiting admin credential", so no admin
// watch may start.
func TestNodeFirstBoot_StartedNodeReconciles(t *testing.T) {
	n := newBareStandaloneNode(t)
	n.bindingPolicy.Store(int32(auth.MTLSBindingEnrolled))
	users, roles := realStores(t)
	result, err := n.FirstBoot(context.Background(), fbDeps(users, roles))
	if err != nil {
		t.Fatalf("FirstBoot: %v", err)
	}
	if !result.AdminCreated {
		t.Fatal("FirstBoot must create the admin on a fresh store")
	}
	if n.adminWatchStarted.Load() {
		t.Fatal("an enrolled-policy node must not start the admin-binding watch")
	}
}

// TestNodeFirstBoot_InitializeRefusalIsReturned: a first-boot dependency set
// with no tenant service is refused by firstboot.Initialize, and the node
// hands that typed refusal back with no result.
func TestNodeFirstBoot_InitializeRefusalIsReturned(t *testing.T) {
	users, roles := realStores(t)
	deps := fbDeps(users, roles)
	deps.Tenants = nil
	result, err := (&Node{}).FirstBoot(context.Background(), deps)
	var missing *firstboot.MissingTenantsError
	if !errors.As(err, &missing) {
		t.Fatalf("err = %v; want *firstboot.MissingTenantsError", err)
	}
	if result != nil {
		t.Fatalf("result = %+v; want nil on refusal", result)
	}
}

// TestReconcileAwaitingAdminCredential_NoProbeIsANoOp: a node with no
// awaiting-admin-credential probe has no posture to decide, even under
// "required", so no admin watch may start.
func TestReconcileAwaitingAdminCredential_NoProbeIsANoOp(t *testing.T) {
	n := &Node{}
	n.bindingPolicy.Store(int32(auth.MTLSBindingRequired))
	users, _ := realStores(t)
	n.reconcileAwaitingAdminCredential(context.Background(), users, nil)
	if n.adminWatchStarted.Load() {
		t.Fatal("a node with no probe must not start the admin-binding watch")
	}
}

// TestServeSealedWindow_SealedNodeWithNoPendingBoot: a sealed node that holds
// no pending standalone or clustered boot configuration has nothing to serve
// the sealed window from, and is refused with a typed configuration error.
func TestServeSealedWindow_SealedNodeWithNoPendingBoot(t *testing.T) {
	barrier, closeBarrier := newTestBarrier(t)
	defer closeBarrier()
	n := &Node{}
	n.barrier.Store(barrier)
	if !n.IsSealed() {
		t.Fatal("precondition: the node must report sealed")
	}
	var cfgErr *ConfigError
	if err := ServeSealedWindow(context.Background(), n); !errors.As(err, &cfgErr) {
		t.Fatalf("err = %v; want *ConfigError", err)
	}
	if cfgErr.Field != "node" {
		t.Fatalf("Field = %q; want \"node\"", cfgErr.Field)
	}
}

// TestNewVolumeRegistry_RefusesANilProvider: an extra provider the registry
// cannot register fails the build with the registry's own typed error.
func TestNewVolumeRegistry_RefusesANilProvider(t *testing.T) {
	registry, err := newVolumeRegistry(volume.Provider(nil))
	var invalid *volume.InvalidProviderError
	if !errors.As(err, &invalid) {
		t.Fatalf("err = %v; want *volume.InvalidProviderError", err)
	}
	if registry != nil {
		t.Fatal("a refused build must return no registry")
	}
}

// TestStartClusterSeal_NothingToMonitor: a node with no cluster-seal holder
// starts no monitor and reports success.
func TestStartClusterSeal_NothingToMonitor(t *testing.T) {
	n := &Node{}
	if err := n.startClusterSeal(context.Background(), nil, nil, nil, nil); err != nil {
		t.Fatalf("startClusterSeal: %v", err)
	}
}

// TestStartMemberRecord_NoFeatureClient: with no feature client to publish
// through, no member record is advertised.
func TestStartMemberRecord_NoFeatureClient(t *testing.T) {
	n := &Node{}
	n.startMemberRecord(nil, qrdbconfig.DefaultConfig())
	if err := n.stopMemberRecord(); err != nil {
		t.Fatalf("stopMemberRecord: %v", err)
	}
	if n.memberAdv.Load() != nil {
		t.Fatal("no advertiser may be published without a feature client")
	}
}

// TestNodeFirstBootWithCeremony_InitializeRefusalIsReturned: with no pending
// credential store there is nothing to resolve, and a dependency set with no
// tenant service is still refused by first boot itself.
func TestNodeFirstBootWithCeremony_InitializeRefusalIsReturned(t *testing.T) {
	users, roles := realStores(t)
	deps := fbDeps(users, roles)
	deps.Tenants = nil
	result, err := (&Node{}).FirstBootWithCeremony(context.Background(), nil, nil, 0, deps)
	var missing *firstboot.MissingTenantsError
	if !errors.As(err, &missing) {
		t.Fatalf("err = %v; want *firstboot.MissingTenantsError", err)
	}
	if result != nil {
		t.Fatalf("result = %+v; want nil on refusal", result)
	}
}

// TestReconcileAwaitingAdminCredential_WatchStartsOnce: a "required" node
// whose first boot created the admin unbound enters "awaiting admin
// credential" and starts the admin-binding watch; a second reconcile keeps
// that posture and does not arm the watch again.
func TestReconcileAwaitingAdminCredential_WatchStartsOnce(t *testing.T) {
	n := newBareStandaloneNode(t)
	n.bindingPolicy.Store(int32(auth.MTLSBindingRequired))
	ctx := context.Background()
	svcs := n.Services()
	if _, err := n.FirstBoot(ctx, firstboot.Dependencies{
		Tenants: svcs.Tenant, Users: n.UserService(), Roles: svcs.Role, RoleResolver: svcs.Role,
		Logger: slog.New(slog.DiscardHandler),
	}); err != nil {
		t.Fatalf("FirstBoot: %v", err)
	}
	if !n.awaitingAdminCredentialProbe.AwaitingAdminCredential() || !n.adminWatchStarted.Load() {
		t.Fatal("an unbound admin under \"required\" must leave the node awaiting, with the watch armed")
	}
	n.reconcileAwaitingAdminCredential(ctx, n.UserService(), n.svcNode.Load().Watch)
	if !n.awaitingAdminCredentialProbe.AwaitingAdminCredential() || !n.adminWatchStarted.Load() {
		t.Fatal("a second reconcile must keep the node awaiting with its one watch")
	}
}

// placementWatcherStub reports no placement changes until its context ends.
type placementWatcherStub struct{}

func (placementWatcherStub) Watch(ctx context.Context) (<-chan placement.Event, error) {
	ch := make(chan placement.Event)
	go func() {
		<-ctx.Done()
		close(ch)
	}()
	return ch, nil
}

// TestReplicaForwardingOption_ClusteredNodeWithNoLogger: a clustered node
// with forwarding on (the default), a placement watcher and a client gets
// the forwarding server option, logging to the default logger when the node
// has none of its own.
func TestReplicaForwardingOption_ClusteredNodeWithNoLogger(t *testing.T) {
	client := newBareStandaloneNode(t).featureClient.Load()
	if client == nil {
		t.Fatal("precondition: a started node holds a feature client")
	}
	n := &Node{}
	ctx := n.background()
	t.Cleanup(n.bgCancel)
	opt, err := n.replicaForwardingOption(qrdbconfig.DefaultConfig(), false, placementWatcherStub{}, client)
	if err != nil {
		t.Fatalf("replicaForwardingOption: %v", err)
	}
	if opt == nil {
		t.Fatal("a clustered node with forwarding on must get the forwarding option")
	}
	if ctx.Err() != nil {
		t.Fatal("the locator runs on the node's background context, which must still be live")
	}
}

// TestBackgroundWork_NothingToRun: a node with no pending boot, no Raft host
// and no lessor or sweeper has nothing to complete or run, so none of these
// may succeed by starting background work.
func TestBackgroundWork_NothingToRun(t *testing.T) {
	n := &Node{}
	if err := n.completeBoot(context.Background()); err != nil {
		t.Fatalf("completeBoot: %v", err)
	}
	if err := n.startLeaseExpiry(nil, nil, nil, nil, 0, qrdbconfig.LeaseConfig{}); err != nil {
		t.Fatalf("startLeaseExpiry: %v", err)
	}
	n.startReclaimSweep(nil, nil, 0)
	if n.bgCtx != nil {
		t.Fatal("no background context may be created when there is nothing to run")
	}
}
