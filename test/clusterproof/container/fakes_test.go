//go:build clusterproof

package container

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/shard"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// testIdentity is a well-formed member identity.
var testIdentity = "spki:" + strings.Repeat("ab", 32)

// fakeRunner records every argv and answers with the first reply whose
// match is in the argv.
type fakeRunner struct {
	mu      sync.Mutex
	runs    [][]string
	replies []reply
}

type reply struct {
	match string
	out   string
	err   error
}

func (r *fakeRunner) Run(_ context.Context, argv []string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runs = append(r.runs, slices.Clone(argv))
	for _, rep := range r.replies {
		if slices.Contains(argv, rep.match) {
			return []byte(rep.out), rep.err
		}
	}
	return nil, nil
}

// fakeNode is one node's API, as every protocol's transport sees it.
type fakeNode struct {
	replicaID uint64
	raft      string

	mu         sync.Mutex
	booted     bool
	bootErr    error // CeremonyInitialize fails with it, after booting if bootAnyway
	bootAnyway bool
	statusErr  error
	ceremonies []transport.CeremonyInitializeRequest
	leader     uint64
	mode       string
	health     string
	topoRaft   string
	shards     []transport.ShardPeers
	adminErr   error
	closed     int
}

// fakeTransport is a connected transport to a fakeNode.
type fakeTransport struct{ n *fakeNode }

var (
	_ transport.Transport = fakeTransport{}
	_ bootAPI             = fakeTransport{}
	_ adminAPI            = fakeTransport{}
)

func (fakeTransport) Connect(context.Context) error                   { return nil }
func (fakeTransport) Healthy(context.Context) bool                    { return true }
func (fakeTransport) Request(context.Context, string, any, any) error { return nil }
func (t fakeTransport) Close() error                                  { t.n.mu.Lock(); t.n.closed++; t.n.mu.Unlock(); return nil }

func (t fakeTransport) BarrierStatus(context.Context) (*transport.BarrierStatusResponse, error) {
	t.n.mu.Lock()
	defer t.n.mu.Unlock()
	if t.n.statusErr != nil {
		return nil, t.n.statusErr
	}
	if t.n.booted {
		return &transport.BarrierStatusResponse{InitializedAt: "2026-10-12T00:00:00Z"}, nil
	}
	return &transport.BarrierStatusResponse{Sealed: true}, nil
}

func (t fakeTransport) CeremonyInitialize(_ context.Context, req *transport.CeremonyInitializeRequest) (*transport.CeremonyInitializeResponse, error) {
	t.n.mu.Lock()
	defer t.n.mu.Unlock()
	c := *req
	c.Secret = slices.Clone(req.Secret)
	t.n.ceremonies = append(t.n.ceremonies, c)
	if t.n.bootErr != nil {
		t.n.booted = t.n.bootAnyway
		return nil, t.n.bootErr
	}
	t.n.booted = true
	return &transport.CeremonyInitializeResponse{}, nil
}

func (t fakeTransport) Health(context.Context) (*transport.HealthResponse, error) {
	t.n.mu.Lock()
	defer t.n.mu.Unlock()
	if t.n.adminErr != nil {
		return nil, t.n.adminErr
	}
	return &transport.HealthResponse{Status: t.n.health, IsLeader: t.n.leader == t.n.replicaID}, nil
}

func (t fakeTransport) Status(context.Context) (*transport.StatusResponse, error) {
	t.n.mu.Lock()
	defer t.n.mu.Unlock()
	return &transport.StatusResponse{RaftAddress: t.n.raft, LeaderID: t.n.leader, IsLeader: t.n.leader == t.n.replicaID}, nil
}

func (t fakeTransport) Peers(context.Context) (*transport.PeersResponse, error) {
	t.n.mu.Lock()
	defer t.n.mu.Unlock()
	return &transport.PeersResponse{Shards: t.n.shards}, nil
}

func (t fakeTransport) Capabilities(context.Context) (*transport.CapabilitiesResponse, error) {
	t.n.mu.Lock()
	defer t.n.mu.Unlock()
	return &transport.CapabilitiesResponse{Mode: t.n.mode}, nil
}

func (t fakeTransport) GetTopology(context.Context) (*transport.TopologyResponse, error) {
	t.n.mu.Lock()
	defer t.n.mu.Unlock()
	return &transport.TopologyResponse{RaftAddress: t.n.topoRaft, ShardCount: 1}, nil
}

func (t fakeTransport) ReplicaStates(context.Context, uint64) (*transport.ReplicaStatesResponse, error) {
	return &transport.ReplicaStatesResponse{}, nil
}

// bareTransport is a transport that carries none of the APIs.
type bareTransport struct{ closed *int }

func (bareTransport) Connect(context.Context) error                   { return nil }
func (bareTransport) Healthy(context.Context) bool                    { return true }
func (bareTransport) Request(context.Context, string, any, any) error { return nil }
func (b bareTransport) Close() error                                  { *b.closed++; return nil }

// fakeCluster is a healthy booted-or-not cluster of fakeNodes behind the
// test inventory, reachable through dial.
type fakeCluster struct {
	inv   Inventory
	nodes map[string]*fakeNode // by Address
	dials []string             // protocol + " " + endpoint
	mu    sync.Mutex
}

func newFakeCluster(t *testing.T) *fakeCluster {
	t.Helper()
	inv := testInventory(t)
	fc := &fakeCluster{inv: inv, nodes: map[string]*fakeNode{}}
	members := make([]transport.PeerInfo, len(inv.Nodes))
	for i, n := range inv.Nodes {
		members[i] = transport.PeerInfo{ReplicaID: n.ReplicaID, Address: n.RaftAddress()}
	}
	for _, n := range inv.Nodes {
		fc.nodes[n.Address] = &fakeNode{
			replicaID: n.ReplicaID, raft: n.RaftAddress(), topoRaft: n.RaftAddress(),
			leader: inv.Nodes[0].ReplicaID, mode: Mode, health: healthy,
			shards: []transport.ShardPeers{{ShardID: shard.ShardKV, Peers: slices.Clone(members)}},
		}
	}
	return fc
}

func (fc *fakeCluster) node(i int) *fakeNode { return fc.nodes[fc.inv.Nodes[i].Address] }

// set applies f to every node under its lock.
func (fc *fakeCluster) set(f func(n *fakeNode)) {
	for _, n := range fc.nodes {
		n.mu.Lock()
		f(n)
		n.mu.Unlock()
	}
}

func (fc *fakeCluster) dial(_ context.Context, protocol, endpoint string) (transport.Transport, error) {
	fc.mu.Lock()
	fc.dials = append(fc.dials, protocol+" "+endpoint)
	fc.mu.Unlock()
	host := strings.TrimPrefix(endpoint, "https://")
	host = host[:strings.LastIndex(host, ":")]
	n, ok := fc.nodes[host]
	if !ok {
		return nil, errors.New("no node at " + endpoint)
	}
	return fakeTransport{n}, nil
}

func (fc *fakeCluster) boot() *Boot {
	return &Boot{Inventory: fc.inv, Dial: fc.dial, Interval: time.Millisecond}
}

// testInventory is a valid three-node host-network inventory whose secret
// files exist.
func testInventory(t *testing.T) Inventory {
	t.Helper()
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	inv := Inventory{
		Image:   "qrdbd:test",
		Docker:  DefaultDocker,
		Network: HostNetwork,
		FirstBoot: FirstBoot{
			SOPINFile:   write("so", "so-pin\n"),
			UserPINFile: write("user", "user-pin\n"),
			SecretFile:  write("secret", "barrier-secret\n"),
		},
	}
	inv.Credentials.CAFile = write("ca.pem", "ca")
	inv.Credentials.CertFile = write("admin.pem", "admin-cert")
	inv.Credentials.KeyFile = filepath.Join(dir, "admin-key.pem")
	for i := 1; i <= 3; i++ {
		id := strconv.Itoa(i)
		inv.Nodes = append(inv.Nodes, Node{
			Name: "n" + id, ReplicaID: uint64(i), Address: "10.0.0." + id,
			TLSDir: "/srv/n" + id + "/tls", Data: "/srv/n" + id + "/data",
			Vars: map[string]string{"host": "h" + id},
		})
	}
	return inv
}
