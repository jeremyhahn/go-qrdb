// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build integration

package integration_test

// Replica forwarding from the embedded client (docs/replica-forwarding): a
// cluster of three embedded nodes, each configured with a different caller,
// and raft databases placed on one node each.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/pkg/volume"
	"github.com/jeremyhahn/go-qrdb/sdk/go/node"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

const (
	efTenant = "ef-t1"
	efPool   = "default"
	efWriter = "ef-writer"
	efReader = "ef-reader"

	// efBudget bounds every wait on the cluster converging.
	efBudget = 2 * time.Minute
)

// The nodes of an efCluster, by the caller each forwards as. The scheduler
// places a single-replica database on the least loaded node, ties going to
// the lowest node ID, so efSpare is the node most databases land on: the
// three nodes that play a part are the ones least likely to hold the replica.
const (
	efSpare = iota // no caller configured
	efWriterNode
	efReaderNode
	efNoCallerNode
	efNodes
)

// efCluster is four embedded nodes, one per role above.
type efCluster struct {
	nodes     []*node.Node
	raftAddrs []string

	// callerCerts are the configured callers' certificates, by username.
	callerCerts map[string]tls.Certificate
	// appRole is the role of the identity the embedding application calls
	// its node's client as (appContext).
	appRole string
}

// efNodeCert issues node i's certificate: a raft member identity and the
// cluster node SAN -- its Raft address, the identity placements name it by --
// that a forward is signed under and a tenant key is delivered to.
func efNodeCert(t *testing.T, ca *qrdbdharness.CA, dir string, i int, raftAddr string) *qrdbdharness.KeyPair {
	t.Helper()
	name := "node-" + strconv.Itoa(i+1)
	kp, err := ca.Issue(filepath.Join(dir, name), qrdbdharness.WithSubject(name, "raft"), qrdbdharness.WithServerAuth(),
		qrdbdharness.WithSPIFFEID(qrdbconfig.DefaultClusterTrustDomain, "/node/"+raftAddr))
	if err != nil {
		t.Fatalf("issue %s: %v", name, err)
	}
	return kp
}

// efCallerCert issues a client certificate for username in efTenant.
func efCallerCert(t *testing.T, ca *qrdbdharness.CA, dir, username string) tls.Certificate {
	t.Helper()
	kp, err := ca.Issue(filepath.Join(dir, username), qrdbdharness.WithSubject(username, efTenant))
	if err != nil {
		t.Fatalf("issue %s: %v", username, err)
	}
	cert, err := tls.X509KeyPair(kp.CertPEM, kp.KeyPEM)
	if err != nil {
		t.Fatalf("load %s: %v", username, err)
	}
	return cert
}

// startEmbeddedFwdCluster boots the four nodes, creates the raft tenant with
// a writer and a reader, and places an anchor database on every node, so
// every node holds the tenant's key and can authenticate its callers.
func startEmbeddedFwdCluster(t *testing.T) *efCluster {
	t.Helper()
	pki := t.TempDir()
	ca, err := qrdbdharness.NewCA(pki)
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	caPEM, err := os.ReadFile(ca.CertPath)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caPEM)
	raftPorts, err := qrdbdharness.ReserveUDPPorts(efNodes)
	if err != nil {
		t.Fatal(err)
	}
	quicPorts, err := qrdbdharness.ReserveUDPPorts(efNodes)
	if err != nil {
		t.Fatal(err)
	}
	adminPorts, err := qrdbdharness.ReserveTCPPorts(efNodes)
	if err != nil {
		t.Fatal(err)
	}

	certs := make([]*qrdbdharness.KeyPair, efNodes)
	c := &efCluster{nodes: make([]*node.Node, efNodes), raftAddrs: make([]string, efNodes)}
	members := map[uint64]string{}
	for i := range efNodes {
		c.raftAddrs[i] = fmt.Sprintf("127.0.0.1:%d", raftPorts[i])
		certs[i] = efNodeCert(t, ca, pki, i, c.raftAddrs[i])
		members[uint64(i+1)] = c.raftAddrs[i]
	}
	// The nodes share a host on distinct admin ports, so each names its
	// peers' admin URLs: the cluster key plane delivers a tenant's key
	// there.
	var peerAdmin []qrdbconfig.PeerAdminEntry
	for i := range efNodes {
		peerAdmin = append(peerAdmin, qrdbconfig.PeerAdminEntry{RaftAddr: c.raftAddrs[i], AdminURL: fmt.Sprintf("https://127.0.0.1:%d", adminPorts[i])})
	}
	c.callerCerts = map[string]tls.Certificate{efWriter: efCallerCert(t, ca, pki, efWriter), efReader: efCallerCert(t, ca, pki, efReader)}
	callers := map[int]node.NodeOption{
		efWriterNode: node.WithEmbeddedCaller(c.callerCerts[efWriter]),
		efReaderNode: node.WithEmbeddedCaller(c.callerCerts[efReader]),
	}

	errs := make([]error, efNodes)
	var wg sync.WaitGroup
	for i := range efNodes {
		cfg := efConfig(t, i, c.raftAddrs[i], quicPorts[i], adminPorts[i], peerAdmin, efPeerIdentities(t, i, certs))
		pair, err := tls.X509KeyPair(certs[i].CertPEM, certs[i].KeyPEM)
		if err != nil {
			t.Fatal(err)
		}
		serverOpts := []server.ServerOption{
			server.WithTLS(&tls.Config{Certificates: []tls.Certificate{pair}, ClientCAs: pool, RootCAs: pool,
				ClientAuth: tls.RequireAndVerifyClientCert, MinVersion: tls.VersionTLS13}),
			server.WithCACertPool(pool),
		}
		nodeOpts := []node.NodeOption{
			node.WithRaftMTLS(&node.RaftMTLSConfig{CACert: caPEM, Cert: certs[i].CertPEM, Key: certs[i].KeyPEM}),
			node.WithTenantIsolation(),
			node.WithDefaultTenantEntitlements(&quota.Entitlements{
				AllowedIsolationModes:     []string{quota.IsolationModeRaft},
				AllowDedicatedRaft:        true,
				AllowSystemManagedBarrier: true,
				AllowedVolumePools:        []string{efPool},
			}),
			node.WithNoSyncWrites(),
		}
		if caller, ok := callers[i]; ok {
			nodeOpts = append(nodeOpts, caller)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.nodes[i], errs[i] = node.NewClusteredNode(context.Background(), cfg, members, serverOpts, nodeOpts...)
		}()
	}
	wg.Wait()
	for _, n := range c.nodes {
		if n != nil {
			t.Cleanup(func() { _ = n.Close() })
		}
	}
	if err := errors.Join(errs...); err != nil {
		t.Fatalf("NewClusteredNode: %v", err)
	}

	c.setUpTenant(t)
	c.createDatabase(t, "ef-anchor", efNodes)
	for i, n := range c.nodes {
		if err := retry(func() error {
			_, err := n.Services().Tenant.GetTenant(context.Background(), efTenant)
			return err
		}); err != nil {
			t.Fatalf("node %d never saw the tenant: %v", i+1, err)
		}
	}
	return c
}

// efConfig is node i's configuration: tenant isolation on a seal of strategy
// none; the QUIC listener, the protocol the embedded client forwards over;
// and REST, which serves the cluster key plane a tenant's key is delivered
// through.
func efConfig(t *testing.T, i int, raftAddr string, quicPort, adminPort int, peerAdmin []qrdbconfig.PeerAdminEntry, memberIdentities string) *qrdbconfig.Config {
	t.Helper()
	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = t.TempDir()
	cfg.Raft.ReplicaID = uint64(i + 1)
	cfg.Raft.RaftAddress = raftAddr
	cfg.Raft.ListenAddress = raftAddr
	cfg.Raft.MemberIdentities = memberIdentities
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendPebble)}}
	cfg.Seal.Enabled = true
	cfg.Seal.Strategy = "none"
	cfg.Storage.VolumePools = []qrdbconfig.VolumePoolConfig{{Name: efPool, Path: t.TempDir()}}
	cfg.Tenant.DefaultQuotas.MaxDatabases = -1
	cfg.Tenant.DefaultQuotas.MaxRaftGroups = -1
	cfg.Tenant.DefaultQuotas.MaxReplicasPerGroup = -1
	cfg.Tenant.DefaultQuotas.MaxTotalReplicas = -1
	cfg.Server.Host = "127.0.0.1"
	cfg.Server.QUICPort = quicPort
	cfg.Server.AdminPort = adminPort
	cfg.Auth.PeerAdminURLs = peerAdmin
	cfg.Protocols.REST = true
	cfg.Protocols.GRPC = false
	cfg.Protocols.QUIC = true
	cfg.Protocols.Unix = false
	cfg.Protocols.MCP = false
	return cfg
}

// efPeerIdentities is node self's raft.member_identities: every other
// member's identity, derived from its certificate.
func efPeerIdentities(t *testing.T, self int, certs []*qrdbdharness.KeyPair) string {
	t.Helper()
	var specs []string
	for i, kp := range certs {
		if i == self {
			continue
		}
		id, err := node.MemberIdentityFromPEM(kp.CertPEM)
		if err != nil {
			t.Fatal(err)
		}
		specs = append(specs, strconv.Itoa(i+1)+"="+id)
	}
	return strings.Join(specs, ",")
}

// eventually retries f, on any node in turn, until it succeeds.
func (c *efCluster) eventually(t *testing.T, what string, f func(n *node.Node) error) {
	t.Helper()
	deadline := time.Now().Add(efBudget)
	for {
		var last error
		for _, n := range c.nodes {
			if last = f(n); last == nil {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: %v", what, last)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// setUpTenant creates the raft tenant, a writer that may read and write keys
// and a reader that may only read them.
func (c *efCluster) setUpTenant(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	// A raft tenant is placed on a catalog pool (docs/volume-pools, D2): the
	// nodes map efPool to a directory, but only the operator's catalog
	// record makes it a pool the tenant can reserve.
	c.eventually(t, "create volume pool", func(n *node.Node) error {
		svc := n.Services()
		catalog, err := volume.NewCatalog(lifecycle.NewVolumeCatalogKVServicer(svc.KV), svc.Transaction, volume.NewRegistry())
		if err != nil {
			return err
		}
		_, err = catalog.CreatePool(ctx, volume.CreatePoolRequest{Name: efPool})
		var exists *volume.PoolAlreadyExistsError
		if errors.As(err, &exists) {
			return nil
		}
		return err
	})
	c.eventually(t, "create tenant", func(n *node.Node) error {
		_, err := n.Services().Tenant.CreateTenant(ctx, efTenant, "Embedded Forwarding", tenant.IsolationRaft, tenant.WithBarrierAutoUnseal(true))
		if errors.Is(err, tenant.ErrTenantAlreadyExists) {
			return nil
		}
		return err
	})
	grants := map[string][]permission.Permission{
		efWriter: {"databases:create", "databases:create_raft", "databases:read", "databases:list", "kv:put", "kv:get"},
		efReader: {"databases:read", "databases:list", "kv:get"},
	}
	for username, perms := range grants {
		var roleID string
		c.eventually(t, "create role for "+username, func(n *node.Node) error {
			r, err := n.Services().Role.CreateRole(ctx, &role.CreateRoleRequest{Name: username + "-role", TenantID: efTenant, Permissions: perms})
			if err != nil {
				return err
			}
			roleID = r.ID
			return nil
		})
		c.eventually(t, "create user "+username, func(n *node.Node) error {
			_, err := n.Services().User.CreateUser(ctx, &user.CreateUserRequest{Username: username, Email: username + "@" + efTenant + ".example.com",
				TenantID: efTenant, RoleIDs: []string{roleID}})
			var exists *user.UsernameExistsError
			if errors.As(err, &exists) {
				return nil
			}
			return err
		})
		// auth.mtls_binding_policy defaults to "required": the node
		// authenticates its configured caller, and so forwards, only once
		// the certificate is enrolled as the user's binding.
		leaf := c.callerCerts[username].Leaf
		c.eventually(t, "bind the certificate of "+username, func(n *node.Node) error {
			tctx := tenant.WithTenantID(ctx, efTenant)
			u, err := n.Services().User.GetUserByUsername(tctx, efTenant, username)
			if err != nil {
				return err
			}
			_, err = n.Services().User.AddCertBinding(tctx, u.ID, user.ComputeSPKIThumbprint(leaf), leaf.NotAfter.Unix())
			return err
		})
	}
	c.eventually(t, "create the application's role", func(n *node.Node) error {
		r, err := n.Services().Role.CreateRole(ctx, &role.CreateRoleRequest{Name: "ef-app-role", TenantID: efTenant,
			Permissions: []permission.Permission{"kv:put", "kv:get"}})
		if err != nil {
			return err
		}
		c.appRole = r.ID
		return nil
	})
}

// appContext is the context the embedding application calls its node's
// client in. The embedded transport authorizes every call against the
// identity its context carries -- the application's -- and a call it
// forwards is made as the node's configured caller, whom the replica
// authorizes.
func (c *efCluster) appContext() context.Context {
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "ef-app", TenantID: efTenant, Roles: []string{c.appRole}})
	return tenant.WithTenantID(ctx, efTenant)
}

// createDatabase creates a raft database of the tenant with replicas
// replicas and returns the indexes of the nodes its placement names.
func (c *efCluster) createDatabase(t *testing.T, name string, replicas int) map[int]bool {
	t.Helper()
	ctx := tenant.WithTenantID(context.Background(), efTenant)
	c.eventually(t, "create database "+name, func(n *node.Node) error {
		_, err := n.Services().Database.CreateForTenant(ctx, database.DatabaseConfig{Name: name, Engine: database.EngineKV, Replicas: replicas})
		var exists *database.AlreadyExistsError
		if errors.As(err, &exists) {
			return nil
		}
		return err
	})
	var placed map[int]bool
	c.eventually(t, "placement of "+name, func(n *node.Node) error {
		p, err := n.Services().PlacementStore.Get(context.Background(), efTenant, name)
		if err != nil {
			return err
		}
		if len(p.Replicas) != replicas {
			return fmt.Errorf("placement names %d replicas, want %d", len(p.Replicas), replicas)
		}
		placed = map[int]bool{}
		for _, r := range p.Replicas {
			i := slices.Index(c.raftAddrs, r.RaftAddr)
			if i < 0 {
				return fmt.Errorf("placement names replica %+v, on no node of the cluster %v", r, c.raftAddrs)
			}
			placed[i] = true
		}
		t.Logf("%s placed on nodes %v", name, placed)
		return nil
	})
	return placed
}

// efPlaced is a single-replica database and the node holding its replica.
type efPlaced struct {
	name    string
	replica int
}

// databasesAvoiding creates single-replica databases until each of nodes
// holds no replica of at least one, and returns such a database for each.
func (c *efCluster) databasesAvoiding(t *testing.T, nodes ...int) map[int]efPlaced {
	t.Helper()
	avoid := map[int]efPlaced{}
	for k := range 12 {
		name := fmt.Sprintf("ef-db-%d", k)
		var replica int
		for i := range c.createDatabase(t, name, 1) {
			replica = i
		}
		for _, i := range nodes {
			if _, ok := avoid[i]; !ok && i != replica {
				avoid[i] = efPlaced{name: name, replica: replica}
			}
		}
		if len(avoid) == len(nodes) {
			return avoid
		}
	}
	t.Fatalf("twelve single-replica databases did not leave nodes %v each a non-replica of one: %v", nodes, avoid)
	return nil
}

// retry calls f until it succeeds or the budget runs out, and returns its
// last error.
func retry(f func() error) error {
	deadline := time.Now().Add(efBudget)
	for {
		err := f()
		if err == nil || time.Now().After(deadline) {
			return err
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// scopedKV is node i's embedded KV client for the database.
func (c *efCluster) scopedKV(t *testing.T, i int, db string) transport.KVClient {
	t.Helper()
	scoped, err := c.nodes[i].Client().DatabaseScoped(db)
	if err != nil {
		t.Fatalf("node %d: scope to %s: %v", i+1, db, err)
	}
	return scoped.KV()
}

func isNotAReplica(err error) bool {
	var r *placement.NotAReplicaError
	return errors.As(err, &r)
}

func isPermissionDenied(err error) bool {
	var reqErr *transport.RequestError
	return errors.As(err, &reqErr) && reqErr.StatusCode == int(wire.StatusPermissionDenied)
}

// TestReplicaForwarding_Embedded: an embedded node holding no replica of a
// raft database forwards a call to the replica as the caller its embedder
// configured, and the replica authorizes that caller -- not the node: the
// writer's write is applied, the reader's is denied while its read is
// served. A node with no caller configured is refused with the typed
// NotAReplica and forwards nothing.
func TestReplicaForwarding_Embedded(t *testing.T) {
	c := startEmbeddedFwdCluster(t)
	ctx := c.appContext()
	avoid := c.databasesAvoiding(t, efWriterNode, efReaderNode, efNoCallerNode)

	t.Run("the writer's write reaches the replica", func(t *testing.T) {
		db := avoid[efWriterNode]
		if err := retry(func() error {
			return c.scopedKV(t, efWriterNode, db.name).Put(ctx, "ef/k1", []byte("from-the-writer"))
		}); err != nil {
			t.Fatalf("the writer's forwarded Put to %s: %v", db.name, err)
		}
		got, err := c.scopedKV(t, db.replica, db.name).Get(ctx, "ef/k1")
		if err != nil || string(got) != "from-the-writer" {
			t.Fatalf("the replica of %s holds %q (%v); want the writer's value", db.name, got, err)
		}
	})

	t.Run("the reader is authorized as the reader", func(t *testing.T) {
		db := avoid[efReaderNode]
		// The writer puts the key the reader reads: through its own replica
		// of the database, or forwarded to it.
		if err := retry(func() error {
			return c.scopedKV(t, efWriterNode, db.name).Put(ctx, "ef/k2", []byte("for-the-reader"))
		}); err != nil {
			t.Fatalf("writer's Put to %s: %v", db.name, err)
		}
		kv := c.scopedKV(t, efReaderNode, db.name)
		var got []byte
		if err := retry(func() (err error) { got, err = kv.Get(ctx, "ef/k2"); return err }); err != nil || string(got) != "for-the-reader" {
			t.Fatalf("the reader's forwarded Get from %s = %q, %v; want the writer's value", db.name, got, err)
		}
		if err := kv.Put(ctx, "ef/k3", []byte("from-the-reader")); !isPermissionDenied(err) {
			t.Fatalf("the reader's forwarded Put = %v; want the replica's permission denial", err)
		}
		if _, err := c.scopedKV(t, db.replica, db.name).Get(ctx, "ef/k3"); err == nil {
			t.Fatal("the reader's denied write was applied")
		}
	})

	t.Run("no caller configured is refused", func(t *testing.T) {
		db := avoid[efNoCallerNode]
		err := c.scopedKV(t, efNoCallerNode, db.name).Put(ctx, "ef/k4", []byte("from-no-one"))
		if !isNotAReplica(err) {
			t.Fatalf("Put from a node with no caller = %v; want *placement.NotAReplicaError", err)
		}
		if _, err := c.scopedKV(t, db.replica, db.name).Get(ctx, "ef/k4"); err == nil {
			t.Fatal("the refused write was applied")
		}
	})
}
