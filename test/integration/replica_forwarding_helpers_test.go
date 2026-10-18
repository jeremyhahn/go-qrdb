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

// The replica-forwarding suites' cluster: real qrdbd nodes -- or, where a
// suite asks, embedded nodes in this process -- one raft tenant whose key
// every node holds, and callers authenticated as that tenant.

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/cert/certtest"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	qrdbnode "github.com/jeremyhahn/go-qrdb/sdk/go/node"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
)

const (
	fwdTrustDomain = "qrdb.cluster"
	fwdTenantID    = "fwd-t1"
	fwdPassphrase  = "integration-test-replica-forwarding-barrier-secret"

	// fwdWriter may read and write keys; fwdNoKV may only list databases.
	fwdWriter = "admin"
	fwdNoKV   = "lister"
)

// fwdNode is one running node: a qrdbd process, or an embedded node in this
// process.
type fwdNode struct {
	cmd      *exec.Cmd
	embedded *qrdbnode.Node
	raftAddr string
	admin    int
	grpc     int
	quic     int
	mcp      int
	unix     string
	stopped  bool
}

// fwdCluster is a running cluster with one raft tenant.
type fwdCluster struct {
	ca    *testCA
	nodes []*fwdNode
	sys   *http.Client

	// creator is the node the tenant was created through: until placement
	// hands the tenant's key to a node, only it holds the tenant.
	creator int

	// writerCert/writerKey authenticate fwdWriter; noKVCert/noKVKey
	// authenticate fwdNoKV; otherCert/otherKey a caller from a tenant that
	// does not exist.
	writerCert, writerKey string
	noKVCert, noKVKey     string
	otherCert, otherKey   string
	tenant                *http.Client

	// writerID and writerRole are fwdWriter's user and role.
	writerID   uint64
	writerRole string

	// admin authenticates as fwdTenantID's own onboarded admin: the system
	// tenant can no longer provision records inside another tenant's IAM at
	// all (owner ruling, 2026-10-01), so createRole and the tenant's bound
	// users are created through this identity instead of sys.
	admin *http.Client
}

// writerContext is a context an embedded node's in-process call is made in as
// fwdWriter: the embedded transport authorizes every call against the
// identity its context carries.
func (c *fwdCluster) writerContext() context.Context {
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{
		Subject: fwdWriter, TenantID: fwdTenantID, Roles: []string{c.writerRole},
	})
	return tenant.WithTenantID(ctx, fwdTenantID)
}

// startFwdCluster starts and unseals n qrdbd nodes, creates the raft tenant
// with a writer and a caller without key permissions, and places one database
// on every node so every node holds the tenant's key and can authenticate its
// callers.
func startFwdCluster(t *testing.T, n int) *fwdCluster {
	t.Helper()
	return startFwdClusterWith(t, n, nil)
}

// fwdEmbedded makes a node of the cluster an embedded node (sdk/go/node) in
// this process instead of a qrdbd process; it returns the node options beyond
// the cluster's own that the node is built with.
type fwdEmbedded func(ca *testCA) []qrdbnode.NodeOption

// startFwdClusterWith is startFwdCluster with the nodes embedded names built
// in this process: the same configuration a qrdbd node runs with, its seal of
// strategy none.
func startFwdClusterWith(t *testing.T, n int, embedded map[int]fwdEmbedded) *fwdCluster {
	t.Helper()
	bin := buildBinary(t)
	ca := generateTestCA(t)
	raftPorts := allocatePorts(t, n)
	adminPorts := allocateTCPPorts(t, n)
	grpcPorts := allocateTCPPorts(t, n)
	quicPorts := allocatePorts(t, n)
	mcpPorts := allocateTCPPorts(t, n)
	peers := buildPeersFlag(raftPorts)

	nodeCerts := make([]string, n)
	nodeKeys := make([]string, n)
	peerAdminURLs := make(map[string]string, n)
	for i := range n {
		raftAddr := fmt.Sprintf("127.0.0.1:%d", raftPorts[i])
		nodeCerts[i], nodeKeys[i] = ca.generateNodeCertWithSPIFFE(t, raftAddr, fwdTrustDomain)
		peerAdminURLs[raftAddr] = fmt.Sprintf("https://127.0.0.1:%d", adminPorts[i])
	}

	var peerAdmin []qrdbconfig.PeerAdminEntry
	for raftAddr, adminURL := range peerAdminURLs {
		peerAdmin = append(peerAdmin, qrdbconfig.PeerAdminEntry{RaftAddr: raftAddr, AdminURL: adminURL})
	}
	members := make(map[uint64]string, n)
	for i := range n {
		members[uint64(i+1)] = fmt.Sprintf("127.0.0.1:%d", raftPorts[i])
	}

	c := &fwdCluster{ca: ca}
	var embeddedWG sync.WaitGroup
	embeddedErrs := make([]error, n)
	dataDirs := make([]string, n)
	for i := range n {
		dataDirs[i] = t.TempDir()
		// A socket path must fit sockaddr_un, so it is not under t.TempDir.
		sockDir, err := os.MkdirTemp("", "qrdb-fwd")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
		node := &fwdNode{raftAddr: fmt.Sprintf("127.0.0.1:%d", raftPorts[i]), admin: adminPorts[i], grpc: grpcPorts[i], quic: quicPorts[i], mcp: mcpPorts[i],
			unix: filepath.Join(sockDir, "qrdbd.sock")}
		if extra, ok := embedded[i]; ok {
			c.nodes = append(c.nodes, node)
			cfg := fwdEmbeddedConfig(t, node, i, dataDirs[i], quicPorts[i], peerAdmin, embeddedPeerIdentities(t, i, nodeCerts))
			opts, serverOpts := fwdEmbeddedOptions(t, ca, nodeCerts[i], nodeKeys[i], extra)
			embeddedWG.Add(1)
			go func() {
				defer embeddedWG.Done()
				node.embedded, embeddedErrs[i] = qrdbnode.NewClusteredNode(context.Background(), cfg, members, serverOpts, opts...)
			}()
			t.Cleanup(func() {
				if node.embedded != nil && !node.stopped {
					_ = node.embedded.Close()
				}
			})
			continue
		}
		node.cmd = spawnNode(t, bin, []string{
			"start",
			"--config", writeIsolationRaftConfig(t, t.TempDir(), peerAdminURLs),
			"--listen", fmt.Sprintf("0.0.0.0:%d", raftPorts[i]),
			"--raft-address", node.raftAddr,
			"--data-dir", dataDirs[i],
			"--sm", "pebble",
			"--discovery", "static",
			"--peers", peers,
			"--member-identities", memberIdentitiesFlag(t, nodeCerts),
			"--shard-id", "1",
			"--replica-id", fmt.Sprintf("%d", i+1),
			"--admin-port", fmt.Sprintf("%d", adminPorts[i]),
			"--grpc-port", fmt.Sprintf("%d", grpcPorts[i]),
			"--enable-quic",
			"--quic-port", fmt.Sprintf("%d", quicPorts[i]),
			"--enable-mcp",
			"--mcp-port", fmt.Sprintf("%d", mcpPorts[i]),
			"--enable-unix",
			"--unix-socket-path", node.unix,
			"--cluster-trust-domain", fwdTrustDomain,
			"--tls-ca-file", ca.CertPath,
			"--tls-cert-file", nodeCerts[i],
			"--tls-key-file", nodeKeys[i],
		}, &logWriter{t: t, prefix: fmt.Sprintf("[fwd-node%d] ", i+1)})
		t.Cleanup(func() {
			if !node.stopped {
				killNode(t, node.cmd)
			}
		})
		c.nodes = append(c.nodes, node)
	}

	var wg sync.WaitGroup
	for i := range n {
		if _, ok := embedded[i]; ok {
			continue
		}
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			waitForBarrierStatusReachable(t, ca, adminPorts[idx], 90*time.Second)
			initAndUnsealBarrier(t, ca, adminPorts[idx], fwdPassphrase)
		}(i)
	}
	wg.Wait()
	embeddedWG.Wait()
	for i, err := range embeddedErrs {
		if err != nil {
			t.Fatalf("embedded node %d: %v", i+1, err)
		}
	}
	for i, dir := range dataDirs {
		if _, ok := embedded[i]; !ok {
			waitForHealthy(t, dir, 60*time.Second)
		}
		waitForAdmin(t, ca.Pool, adminPorts[i], 90*time.Second)
	}
	leaderPort := waitForLeaderViaAdmin(t, ca, adminPorts, 60*time.Second)
	warmupWrite(t, ca, leaderPort, 15*time.Second)
	for i, port := range adminPorts {
		if port == leaderPort {
			c.creator = i
		}
	}
	// The tenant is created through a qrdbd node: an embedded node takes its
	// tenant defaults from its node options, not the configuration file the
	// qrdbd nodes read, and the suite proves an embedded node receives a
	// tenant's key rather than creates it. A non-leader forwards the write.
	if _, embeddedCreator := embedded[c.creator]; embeddedCreator {
		for i := range n {
			if _, ok := embedded[i]; !ok {
				c.creator = i
				break
			}
		}
	}

	c.sys = mtlsHTTPClient(t, ca, 15*time.Second)
	base := fmt.Sprintf("https://127.0.0.1:%d", adminPorts[c.creator])
	// A raft tenant is placed on a catalog pool (docs/volume-pools, D2):
	// every node maps "default" to a directory, but only the operator's
	// catalog record makes it a pool a tenant can reserve.
	createVolumePoolREST(t, c.sys, adminPorts[c.creator], "default")
	// A tenant is created with its first admin, or not at all (go-qrdb
	// tenant onboarding): the admin keeps its own key, only the CSR crosses
	// the wire.
	adminKeyPEM, adminCSRPEM, err := certtest.NewCSR(fwdTenantID + "-admin")
	if err != nil {
		t.Fatalf("certtest.NewCSR: %v", err)
	}
	tenantBody, st := iraftAPIPost(t, c.sys, base+"/v1/tenants", map[string]interface{}{
		"id": fwdTenantID, "name": "Replica Forwarding Tenant",
		"isolation_type": "raft", "barrier_auto_unseal": true,
		"admin_username": fwdTenantID + "-admin", "admin_csr_pem": string(adminCSRPEM),
	})
	if st != http.StatusCreated && st != http.StatusOK {
		t.Fatalf("create tenant through node %d: status %d, body %s", c.creator+1, st, tenantBody)
	}
	// The system tenant can no longer provision records inside another
	// tenant's IAM at all (owner ruling, 2026-10-01), so createRole and the
	// tenant's bound users below authenticate as the tenant's own onboarded
	// admin; its built-in Admin role cannot create a NEW role (RolesCreate
	// is SO-exclusive), so it self-assigns its own pre-seeded SO role first.
	var createResp struct {
		Admin struct {
			CertPEM string `json:"cert_pem"`
		} `json:"admin"`
	}
	if err := json.Unmarshal(tenantBody, &createResp); err != nil || createResp.Admin.CertPEM == "" {
		t.Fatalf("decode tenant create admin cert: %v body=%s", err, tenantBody)
	}
	adminTLSCert, err := tls.X509KeyPair([]byte(createResp.Admin.CertPEM), adminKeyPEM)
	if err != nil {
		t.Fatalf("X509KeyPair for the onboarded admin: %v", err)
	}
	c.admin = qrdbdharness.HTTPSClient(ca.Pool, &adminTLSCert, 15*time.Second)
	selfAssignTenantSO(t, adminPorts[c.creator], fwdTenantID, c.admin)
	writerRole := c.createRole(t, base, "kv-writer", "databases:create", "databases:create_raft", "databases:read",
		"databases:list", "databases:delete", "kv:put", "kv:get", "kv:delete", "watch:watch")
	listerRole := c.createRole(t, base, "db-lister", "databases:list")
	// The certs below authenticate as CN=admin/CN=lister, OU=fwd-t1, signed
	// by this suite's own harness CA (not the tenant's Key B login CA the
	// onboarded admin's CSR was signed by above) -- a BYOC-shaped identity
	// distinct from the pending-admin account OnboardTenant created. Under
	// the default auth.mtls_binding_policy "required" it authenticates
	// nothing until its own account exists and its SPKI thumbprint is
	// explicitly bound (createBoundUser, the same pattern
	// cluster_enable_encryption_test.go's tenantClient uses), so the certs
	// are minted BEFORE the user that binds them.
	c.writerCert, c.writerKey = ca.generateClientCertForTenant(t, fwdTenantID)
	kp := ca.issue(t, t.TempDir(), qrdbdharness.WithSubject(fwdNoKV, fwdTenantID))
	c.noKVCert, c.noKVKey = kp.CertPath, kp.KeyPath
	kp = ca.issue(t, t.TempDir(), qrdbdharness.WithSubject(fwdWriter, "fwd-unknown-tenant"))
	c.otherCert, c.otherKey = kp.CertPath, kp.KeyPath
	// auth.mtls_binding_policy defaults to "required": a tenant user's
	// certificate authenticates only once it is enrolled as their binding.
	newUser := func(username, roleID, certPath string) uint64 {
		return createBoundUser(t, c.admin, adminPorts[c.creator], map[string]any{
			"username": username, "email": username + "@" + fwdTenantID + ".example.com",
			"tenant_id": fwdTenantID, "role_ids": []string{roleID},
		}, certPath)
	}
	c.writerID, c.writerRole = newUser(fwdWriter, writerRole, c.writerCert), writerRole
	newUser(fwdNoKV, listerRole, c.noKVCert)
	c.tenant = mtlsHTTPClientFromFiles(t, ca, c.writerCert, c.writerKey, 30*time.Second)

	c.createDatabase(t, "fwd-anchor", n)
	for i := range c.nodes {
		c.waitForTenantOn(t, i)
	}
	return c
}

// createRole creates a tenant role with perms and returns its ID.
func (c *fwdCluster) createRole(t *testing.T, base, name string, perms ...string) string {
	t.Helper()
	body, st := iraftAPIPost(t, c.admin, base+"/roles", map[string]interface{}{
		"name": name, "tenant_id": fwdTenantID, "permissions": perms,
	})
	if st != http.StatusCreated && st != http.StatusOK {
		t.Fatalf("create role %s: status %d, body %s", name, st, body)
	}
	var role struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &role); err != nil || role.ID == "" {
		t.Fatalf("decode role id: %s", body)
	}
	return role.ID
}

// waitForTenantOn waits until node i authenticates the tenant's callers.
func (c *fwdCluster) waitForTenantOn(t *testing.T, i int) {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/v1/databases/list", c.nodes[i].admin)
	var body []byte
	var st int
	if !pollUntil(120*time.Second, 500*time.Millisecond, func() bool {
		body, st = iraftAPIGet(t, c.tenant, url)
		if st != http.StatusOK && st != http.StatusServiceUnavailable {
			t.Fatalf("tenant not served by node %d: status %d, body %s", i+1, st, body)
		}
		return st == http.StatusOK
	}) {
		t.Fatalf("tenant not served by node %d: status %d, body %s", i+1, st, body)
	}
}

// createDatabase creates a raft database with the given replica count
// through the node the tenant was created through, and returns its shard ID.
func (c *fwdCluster) createDatabase(t *testing.T, name string, replicas int) uint64 {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/v1/databases", c.nodes[c.creator].admin)
	var body []byte
	var st int
	var shardID uint64
	if !pollUntil(90*time.Second, 500*time.Millisecond, func() bool {
		body, st = iraftAPIPost(t, c.tenant, url, map[string]interface{}{
			"name": name, "backend": "pebble", "replicas": replicas,
		})
		if st == http.StatusCreated || st == http.StatusOK {
			var info struct {
				ShardID uint64 `json:"shardId"`
			}
			if err := json.Unmarshal(body, &info); err != nil || info.ShardID == 0 {
				t.Fatalf("create %s: no shard ID in %s", name, body)
			}
			shardID = info.ShardID
			return true
		}
		if st != http.StatusServiceUnavailable {
			t.Fatalf("create %s: status %d, body %s", name, st, body)
		}
		return false
	}) {
		t.Fatalf("create %s: status %d, body %s", name, st, body)
	}
	return shardID
}

// live returns the indexes of the nodes still running.
func (c *fwdCluster) live() []int {
	var out []int
	for i, n := range c.nodes {
		if !n.stopped {
			out = append(out, i)
		}
	}
	return out
}

// shardPlacement is where a shard runs: the nodes holding a replica, their
// replica IDs, and the node of its current leader (-1 while it has none).
type shardPlacement struct {
	replicas  map[int]uint64
	leader    int
	leaderRID uint64
}

// placementOf reads the shard's replicas and leader from every live node's
// topology, waiting until the shard runs on want replicas and has a leader
// among them.
func (c *fwdCluster) placementOf(t *testing.T, shardID uint64, want int) shardPlacement {
	t.Helper()
	var p shardPlacement
	if !pollUntil(120*time.Second, 500*time.Millisecond, func() bool {
		p = shardPlacement{replicas: map[int]uint64{}, leader: -1}
		for _, i := range c.live() {
			body, st := iraftAPIGet(t, c.sys, fmt.Sprintf("https://127.0.0.1:%d/admin/topology", c.nodes[i].admin))
			if st != http.StatusOK {
				continue
			}
			var topo struct {
				Shards []struct {
					ShardID   uint64 `json:"shard_id"`
					ReplicaID uint64 `json:"replica_id"`
					IsLeader  bool   `json:"is_leader"`
				} `json:"shards"`
			}
			if err := json.Unmarshal(body, &topo); err != nil {
				t.Fatalf("decode topology of node %d: %v", i+1, err)
			}
			for _, s := range topo.Shards {
				if s.ShardID != shardID {
					continue
				}
				p.replicas[i] = s.ReplicaID
				if s.IsLeader {
					p.leader, p.leaderRID = i, s.ReplicaID
				}
			}
		}
		return len(p.replicas) == want && p.leader >= 0
	}) {
		t.Fatalf("shard %d: placement %+v; want %d replicas with a leader", shardID, p, want)
	}
	return p
}

// nonReplicas returns the live nodes holding no replica in p.
func (c *fwdCluster) nonReplicas(p shardPlacement) []int {
	var out []int
	for _, i := range c.live() {
		if _, ok := p.replicas[i]; !ok {
			out = append(out, i)
		}
	}
	return out
}

// stop kills node i, as a crash; an embedded node is closed.
func (c *fwdCluster) stop(t *testing.T, i int) {
	t.Helper()
	n := c.nodes[i]
	if n.embedded != nil {
		_ = n.embedded.Close()
		n.stopped = true
		return
	}
	killNode(t, n.cmd)
	n.stopped = true
}

// fwdEmbeddedConfig is an embedded node's configuration: what
// writeIsolationRaftConfig and the qrdbd flags give a qrdbd node of the
// cluster, with a seal of strategy none, and the API listeners the suite
// reaches it on.
func fwdEmbeddedConfig(t *testing.T, n *fwdNode, i int, dataDir string, quicPort int, peerAdmin []qrdbconfig.PeerAdminEntry, memberIdentities string) *qrdbconfig.Config {
	t.Helper()
	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = dataDir
	cfg.Raft.ReplicaID = uint64(i + 1)
	cfg.Raft.RaftAddress = n.raftAddr
	cfg.Raft.ListenAddress = n.raftAddr
	cfg.Raft.MemberIdentities = memberIdentities
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendPebble)}}
	cfg.Seal.Enabled = true
	cfg.Seal.Strategy = "none"
	cfg.Storage.VolumePools = []qrdbconfig.VolumePoolConfig{{Name: "default", Path: t.TempDir()}}
	cfg.Tenant.DefaultEntitlements.AllowedIsolationModes = []string{"prefix", "raft"}
	cfg.Tenant.DefaultEntitlements.AllowDedicatedRaft = true
	cfg.Tenant.DefaultEntitlements.AllowSystemManagedBarrier = true
	cfg.Tenant.DefaultEntitlements.AllowDedicatedWAL = true
	cfg.Tenant.DefaultEntitlements.AllowedVolumePools = []string{"default"}
	cfg.Tenant.DefaultQuotas.MaxDatabases = -1
	cfg.Tenant.DefaultQuotas.MaxRaftGroups = -1
	cfg.Tenant.DefaultQuotas.MaxReplicasPerGroup = -1
	cfg.Tenant.DefaultQuotas.MaxTotalReplicas = -1
	cfg.Tenant.DefaultQuotas.MaxDedicatedWALGroups = -1
	cfg.Auth.PeerAdminURLs = peerAdmin
	cfg.Auth.ClusterTrustDomain = fwdTrustDomain
	cfg.Server.Host = "127.0.0.1"
	cfg.Server.AdminPort = n.admin
	cfg.Server.GRPCPort = n.grpc
	cfg.Server.QUICPort = quicPort
	cfg.Protocols.REST = true
	cfg.Protocols.GRPC = true
	cfg.Protocols.QUIC = true
	cfg.Protocols.Unix = false
	cfg.Protocols.MCP = false
	return cfg
}

// fwdEmbeddedOptions returns an embedded node's node and server options: its
// node certificate as its Raft and API identity, the cluster CA, raft tenant
// isolation, and extra's.
func fwdEmbeddedOptions(t *testing.T, ca *testCA, certPath, keyPath string, extra fwdEmbedded) ([]qrdbnode.NodeOption, []server.ServerOption) {
	t.Helper()
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	caPEM, err := os.ReadFile(ca.CertPath)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	opts := append([]qrdbnode.NodeOption{
		qrdbnode.WithRaftMTLS(&qrdbnode.RaftMTLSConfig{CACert: caPEM, Cert: certPEM, Key: keyPEM}),
		qrdbnode.WithTenantIsolation(),
		qrdbnode.WithNoSyncWrites(),
	}, extra(ca)...)
	serverOpts := []server.ServerOption{
		server.WithTLS(&tls.Config{Certificates: []tls.Certificate{pair}, ClientCAs: ca.Pool, RootCAs: ca.Pool,
			ClientAuth: tls.VerifyClientCertIfGiven, MinVersion: tls.VersionTLS13}),
		server.WithCACertPool(ca.Pool),
	}
	return opts, serverOpts
}

// embeddedPeerIdentities is an embedded node's raft.member_identities: every
// other member's identity, derived from its certificate.
func embeddedPeerIdentities(t *testing.T, self int, certPaths []string) string {
	t.Helper()
	all := strings.Split(memberIdentitiesFlag(t, certPaths), ",")
	return strings.Join(append(all[:self:self], all[self+1:]...), ",")
}

// fwdRetryBudget bounds retry: a tenant's databases are still starting when
// the first forwarded call reaches them, and a bounded read answers a slow
// ReadIndex with a retryable error.
const fwdRetryBudget = 2 * time.Minute

// retry calls f until it succeeds or the budget runs out, and returns its
// last error.
func retry(f func() error) error {
	var err error
	pollUntil(fwdRetryBudget, 250*time.Millisecond, func() bool {
		err = f()
		return err == nil
	})
	return err
}
