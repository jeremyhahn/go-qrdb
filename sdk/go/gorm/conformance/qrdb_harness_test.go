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

//go:build gormconformance

package tests_test

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"strconv"
	"time"

	gomysql "github.com/go-sql-driver/mysql"

	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
)

// shared holds the qrdbd subprocess this test binary spawned for the mysql
// or pgsql target, populated by startWireServer and torn down by
// TestMain. It is nil for the node target, which needs no subprocess.
var shared *wireServer

const (
	// sqlTenant is the tenant the GORM client certificate authenticates as
	// (OU=gorm) and therefore the tenant every wire session resolves its
	// services through. It does NOT exist at first boot — qrdbd bootstraps
	// only the system tenant — so startWireServer provisions it explicitly;
	// see provision.
	sqlTenant = "gorm"

	// sqlUser is the GORM client certificate's CommonName, which qrdb's mTLS
	// authenticator maps to the user name and both wire protocols carry as
	// the connection's user.
	sqlUser = "gorm"

	// sqlDatabase is the database both wire DSNs select. A database is owned
	// by exactly one tenant, and the (tenant, database) pair is what the
	// service registry resolves, so this one is created BY the gorm identity
	// (see provision) rather than by qrdbd's --sql-database startup
	// path, which would make it system-owned and therefore invisible to the
	// gorm tenant.
	sqlDatabase = "gorm"

	// bootDatabase is the name passed to qrdbd's --sql-database. It is the
	// system-owned default the wire servers fall back to for a connection
	// that names no database, deliberately NOT sqlDatabase: qrdbd creates
	// --sql-database during startup under the system tenant, which would
	// take the name the gorm tenant needs to own.
	bootDatabase = "qrdb"

	// nodeCommonName is the CommonName on the certificate qrdbd itself
	// presents. It is issued into the system tenant because it is the node's
	// own platform identity, not a data-plane one.
	nodeCommonName = "qrdb-gorm-conformance-node"

	// systemTenant is qrdb's bootstrap tenant, the scope the admin identity
	// this harness performs provisioning with belongs to.
	systemTenant = "system"

	// adminCommonName is the CommonName of the identity firstboot provisions
	// with platform-wide authority. The harness authenticates its readiness
	// polling and its tenant provisioning as this identity; the GORM client
	// certificate is a separate, ordinary tenant-scoped identity and is the
	// only one openMySQL/openPgSQL ever use.
	adminCommonName = "admin"

	// nodeHostTimeout bounds the wait for qrdbd's Raft node host to
	// materialize its host.id file.
	nodeHostTimeout = 30 * time.Second

	// healthTimeout bounds the wait for the admin server's /health endpoint.
	healthTimeout = 90 * time.Second

	// leaderTimeout bounds the wait for shard 1 to elect itself leader.
	leaderTimeout = 60 * time.Second

	// listenerTimeout bounds the wait for a wire protocol port to accept a
	// TCP connection.
	listenerTimeout = 60 * time.Second

	// listenerPollInterval is how often the wire listener wait re-dials.
	listenerPollInterval = 50 * time.Millisecond

	// dialTimeout bounds a single readiness dial against a wire port.
	dialTimeout = 2 * time.Second

	// provisionTimeout bounds one qrdb CLI provisioning invocation.
	provisionTimeout = "30s"

	// tenantDisplayName is the human-readable name given to the provisioned
	// tenant.
	tenantDisplayName = "GORM conformance"

	// provisioningRoleName is the role bound to the GORM identity so it may
	// create and configure its own database.
	provisioningRoleName = "gorm-conformance-provisioner"

	// provisioningPermissions is the --permissions value for that role: the
	// minimum the two provisioning calls need (see pkg/permission's
	// DatabasesCreate and DatabasesUpdate, the same strings an operator types
	// on the command line). Nothing data-plane is granted — the SQL wire path
	// resolves services through the session's tenant rather than a
	// per-statement permission check.
	provisioningPermissions = "databases:create,databases:update"
)

// errNoRoleID reports a `roles create` response that decoded but carried no
// role ID, which would silently produce a user with no effective permissions.
var errNoRoleID = errors.New("response contained no role id")

// wireServerError reports a failure while bringing up the qrdbd subprocess
// the mysql and pgsql targets run against.
type wireServerError struct {
	Op  string
	Err error
}

// Error returns a human-readable description.
func (e *wireServerError) Error() string {
	return "gorm-conformance: start wire server: " + e.Op + ": " + e.Err.Error()
}

// Unwrap exposes the underlying cause.
func (e *wireServerError) Unwrap() error { return e.Err }

// provisionError reports a failed `qrdb` CLI provisioning invocation, carrying
// the CLI's combined output because that is where the server's own diagnostic
// ends up.
type provisionError struct {
	Op     string
	Output string
	Err    error
}

// Error returns a human-readable description.
func (e *provisionError) Error() string {
	return "gorm-conformance: provision " + e.Op + ": " + e.Err.Error() + "\noutput: " + e.Output
}

// Unwrap exposes the underlying cause.
func (e *provisionError) Unwrap() error { return e.Err }

// listenerTimeoutError reports a wire protocol port that never accepted a
// connection.
type listenerTimeoutError struct {
	Protocol string
	Addr     string
	Timeout  time.Duration
	LastErr  error
}

// Error returns a human-readable description.
func (e *listenerTimeoutError) Error() string {
	msg := "gorm-conformance: " + e.Protocol + " listener at " + e.Addr +
		" did not accept a connection within " + e.Timeout.String()
	if e.LastErr != nil {
		msg += ": " + e.LastErr.Error()
	}
	return msg
}

// Unwrap exposes the underlying cause.
func (e *listenerTimeoutError) Unwrap() error { return e.LastErr }

// wireServerNotStartedError reports an opener invoked before TestMain
// populated shared, which can only happen if the target dispatch table and
// TestMain's subprocess branch fall out of sync.
type wireServerNotStartedError struct {
	Target target
}

// Error returns a human-readable description.
func (e *wireServerNotStartedError) Error() string {
	return "gorm-conformance: no wire server started for target " + string(e.Target)
}

// wireServer is a running qrdbd subprocess with the MySQL and PostgreSQL wire
// protocol servers enabled, plus the mTLS material needed to reach them. It is
// the wire-target analogue of the node target's in-process dialector: a single
// shared server for the whole run, not one per test.
//
// Every primitive it needs — CA and certificate minting, non-ephemeral port
// reservation, the qrdbd/qrdb builds, subprocess lifecycle and the readiness
// waits — comes from sdk/go/testutil/qrdbdharness, the same package
// test/integration drives its daemons with.
type wireServer struct {
	daemon *qrdbdharness.Daemon

	// workDir holds the CA and every leaf certificate; dataDir is qrdbd's
	// --data-dir. Both are removed by stop.
	workDir string
	dataDir string

	ca *qrdbdharness.CA

	adminPort int
	mysqlPort int
	pgsqlPort int

	// client is the GORM identity's key pair: CN=gorm (user), OU=gorm
	// (tenant). qrdb's wire servers are mTLS-only with no password path, so
	// this certificate IS the credential both DSNs authenticate with.
	client *qrdbdharness.KeyPair

	// mysqlTLSConfigName is the name this run registered its TLS config
	// under via go-sql-driver/mysql's RegisterTLSConfig, referenced from the
	// mysql DSN as tls=<name>.
	mysqlTLSConfigName string
}

// stop terminates the qrdbd subprocess, releases the registered MySQL TLS
// config, and removes the directories the harness created. Safe to call on a
// nil *wireServer.
func (s *wireServer) stop() {
	if s == nil {
		return
	}
	if s.daemon != nil {
		_ = s.daemon.Stop()
	}
	if s.mysqlTLSConfigName != "" {
		gomysql.DeregisterTLSConfig(s.mysqlTLSConfigName)
	}
	if s.workDir != "" {
		_ = os.RemoveAll(s.workDir)
	}
	if s.dataDir != "" {
		_ = os.RemoveAll(s.dataDir)
	}
}

// startWireServer builds qrdbd and the qrdb CLI, mints a CA and the three leaf
// certificates the run needs, starts a single-node qrdbd with both wire
// protocol servers enabled, waits for it to elect a leader, provisions the
// tenant the GORM identity authenticates into, and registers the TLS config
// the mysql DSN refers to.
//
// The daemon is built with qrdbdharness.DefaultBuildTags, which includes
// qrdb_mysql and qrdb_pgsql: without those tags --enable-mysql/--enable-pgsql
// parse fine and then silently start nothing, because pkg/server's
// startMySQLFn/startPgSQLFn hooks are registered from files behind exactly
// those tags.
func startWireServer(tgt target) (*wireServer, error) {
	daemonBin, err := qrdbdharness.BuildBinary(qrdbdharness.DaemonPackage)
	if err != nil {
		return nil, &wireServerError{Op: "build qrdbd", Err: err}
	}
	cliBin, err := qrdbdharness.BuildBinary(qrdbdharness.ClientPackage)
	if err != nil {
		return nil, &wireServerError{Op: "build qrdb CLI", Err: err}
	}

	workDir, err := os.MkdirTemp("", "qrdb-gorm-conformance-pki-*")
	if err != nil {
		return nil, &wireServerError{Op: "create PKI directory", Err: err}
	}
	srv := &wireServer{workDir: workDir}

	ca, err := qrdbdharness.NewCA(workDir)
	if err != nil {
		srv.stop()
		return nil, &wireServerError{Op: "generate CA", Err: err}
	}
	srv.ca = ca

	// The node leaf names no tenant, as a production node leaf does not: a
	// tenant is named by an OU together with an agreeing SPIFFE ID SAN, and
	// an OU alone is refused.
	node, err := ca.Issue(workDir,
		qrdbdharness.WithSubject(nodeCommonName, ""),
		qrdbdharness.WithServerAuth())
	if err != nil {
		srv.stop()
		return nil, &wireServerError{Op: "issue node certificate", Err: err}
	}
	admin, err := ca.Issue(workDir, qrdbdharness.WithSubject(adminCommonName, systemTenant))
	if err != nil {
		srv.stop()
		return nil, &wireServerError{Op: "issue admin certificate", Err: err}
	}
	client, err := ca.Issue(workDir, qrdbdharness.WithSubject(sqlUser, sqlTenant))
	if err != nil {
		srv.stop()
		return nil, &wireServerError{Op: "issue gorm client certificate", Err: err}
	}
	srv.client = client

	// Raft speaks QUIC, so its listen port is drawn from the UDP side of the
	// same reservation table the TCP ports come from.
	raftPort, err := qrdbdharness.ReserveUDPPort()
	if err != nil {
		srv.stop()
		return nil, &wireServerError{Op: "reserve raft port", Err: err}
	}
	tcpPorts, err := qrdbdharness.ReserveTCPPorts(4)
	if err != nil {
		srv.stop()
		return nil, &wireServerError{Op: "reserve TCP ports", Err: err}
	}
	srv.adminPort, srv.mysqlPort, srv.pgsqlPort = tcpPorts[0], tcpPorts[2], tcpPorts[3]
	grpcPort := tcpPorts[1]

	dataDir, err := os.MkdirTemp("", "qrdb-gorm-conformance-data-*")
	if err != nil {
		srv.stop()
		return nil, &wireServerError{Op: "create data directory", Err: err}
	}
	srv.dataDir = dataDir

	// --sm mvcc is load-bearing, not incidental: qrdb's whole relational
	// stack is built on MVCC revisions (the table catalog is a revision-keyed
	// compare-and-swap, an interactive transaction commits its buffered
	// write-set as one revision-guarded CAS, AUTO_INCREMENT allocates from a
	// Raft-committed sequence the same way). Under the default "pebble" state
	// machine, or "memory", the very first CREATE TABLE fails with
	// "statemachine: mvcc not configured". See test/integration/sql_test.go's
	// startThreeNodeClusterWithSQL, which makes the same choice for the same
	// reason.
	args := []string{
		"start",
		"--listen", net.JoinHostPort("0.0.0.0", strconv.Itoa(raftPort)),
		"--raft-address", net.JoinHostPort("127.0.0.1", strconv.Itoa(raftPort)),
		"--data-dir", dataDir,
		"--sm", "mvcc",
		"--discovery", "static",
		"--peers", "1=" + net.JoinHostPort("127.0.0.1", strconv.Itoa(raftPort)),
		"--shard-id", "1",
		"--replica-id", "1",
		"--admin-port", strconv.Itoa(srv.adminPort),
		"--grpc-port", strconv.Itoa(grpcPort),
		"--enable-mysql", "--mysql-port", strconv.Itoa(srv.mysqlPort),
		"--enable-pgsql", "--pgsql-port", strconv.Itoa(srv.pgsqlPort),
		"--sql-database", bootDatabase,
	}
	args = append(args, ca.TLSFlags(node)...)

	daemon, err := qrdbdharness.Start(daemonBin, args,
		qrdbdharness.WithOutput(qrdbdharness.NewPrefixWriter(os.Stderr, "[gorm-conformance-"+string(tgt)+"] ")))
	if err != nil {
		srv.stop()
		return nil, &wireServerError{Op: "start qrdbd", Err: err}
	}
	srv.daemon = daemon

	if err := qrdbdharness.WaitForNodeHost(dataDir, nodeHostTimeout); err != nil {
		srv.stop()
		return nil, &wireServerError{Op: "wait for node host", Err: err}
	}
	if err := qrdbdharness.WaitForHTTPOK(srv.adminURL("/health"), ca.Pool, nil, healthTimeout); err != nil {
		srv.stop()
		return nil, &wireServerError{Op: "wait for /health", Err: err}
	}
	adminCert, err := admin.TLSCertificate()
	if err != nil {
		srv.stop()
		return nil, &wireServerError{Op: "load admin certificate", Err: err}
	}
	if err := qrdbdharness.WaitForLeader(srv.adminURL("/status"), ca.Pool, &adminCert, leaderTimeout); err != nil {
		srv.stop()
		return nil, &wireServerError{Op: "wait for leader", Err: err}
	}

	if err := srv.provision(cliBin, admin); err != nil {
		srv.stop()
		return nil, err
	}

	if err := waitForListener("mysql", srv.mysqlPort); err != nil {
		srv.stop()
		return nil, &wireServerError{Op: "wait for mysql listener", Err: err}
	}
	if err := waitForListener("pgsql", srv.pgsqlPort); err != nil {
		srv.stop()
		return nil, &wireServerError{Op: "wait for pgsql listener", Err: err}
	}

	if err := srv.registerMySQLTLS(tgt); err != nil {
		srv.stop()
		return nil, err
	}

	return srv, nil
}

// adminURL builds a URL for the admin/REST server's path.
func (s *wireServer) adminURL(path string) string {
	return "https://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(s.adminPort)) + path
}

// provision creates the tenant and database the GORM wire sessions resolve
// through, driving the shipped `qrdb` CLI over REST — the same route an
// operator would take, and the one test/integration's tenant and database
// suites drive — rather than renaming things to dodge the tenant boundary.
//
// Two facts make this necessary:
//
//   - qrdbd bootstraps exactly ONE tenant at first boot, "system", while the
//     GORM identity is CN=gorm, OU=gorm and qrdb's mTLS authenticator maps OU
//     to the tenant every wire session resolves its services through. Without
//     the tenant, every statement fails with
//     "service: tenant not found: gorm".
//   - A database is owned by exactly one tenant, and the service registry
//     resolves the (tenant, database) pair, so a database created by qrdbd's
//     own --sql-database startup path (system-owned) is invisible to the gorm
//     tenant: "service: database not found: gorm (tenant: gorm)". The
//     database is therefore created BY the gorm identity, which is what
//     stamps its owner, and then has both wire protocols enabled on it so
//     pkg/sql/provider's protocol filter admits it. Creating it as the gorm
//     identity in turn needs a role and a user record, because every qrdb
//     transport is default-deny — see grantProvisioningRole.
func (s *wireServer) provision(cli string, admin *qrdbdharness.KeyPair) error {
	if out, err := s.runCLI(cli, admin,
		"tenant", "create", "--id", sqlTenant, "--name", tenantDisplayName); err != nil {
		return &provisionError{Op: "tenant create " + sqlTenant, Output: out, Err: err}
	}

	roleID, err := s.grantProvisioningRole(cli, admin)
	if err != nil {
		return err
	}
	// The gorm identity authenticates by its mTLS certificate alone, so it is
	// a certificate-only user with no email login key.
	if out, err := s.runCLI(cli, admin, "users", "create",
		"--username", sqlUser, "--tenant-id", sqlTenant, "--roles", roleID,
		"--without-email-login-key"); err != nil {
		return &provisionError{Op: "users create " + sqlUser, Output: out, Err: err}
	}

	if out, err := s.runCLI(cli, s.client, "database", "create", sqlDatabase,
		"--engine", "kv", "--backend", "pebble", "--sql-concurrency", sqlConcurrency); err != nil {
		return &provisionError{Op: "database create " + sqlDatabase, Output: out, Err: err}
	}
	if out, err := s.runCLI(cli, s.client, "database", "update-protocol", sqlDatabase,
		"--mysql", "--postgresql"); err != nil {
		return &provisionError{Op: "database update-protocol " + sqlDatabase, Output: out, Err: err}
	}
	return nil
}

// grantProvisioningRole creates the role whose permissions let the GORM
// identity provision its own database, returning the role's generated ID for
// binding to the user.
//
// Every qrdb transport is default-deny: a freshly created tenant identity
// carries no permissions at all until a role is bound to a user record whose
// username matches the certificate's CommonName (the auth middleware enriches
// the mTLS identity from the user store — see pkg/auth/middleware). The grant
// is deliberately the minimum the two provisioning calls need; the SQL wire
// path itself resolves services through the tenant, not through a per-
// statement permission check, so no data-plane permission is granted here.
func (s *wireServer) grantProvisioningRole(cli string, admin *qrdbdharness.KeyPair) (string, error) {
	out, err := s.runCLI(cli, admin, "--output", "json", "roles", "create",
		"--name", provisioningRoleName,
		"--tenant-id", sqlTenant,
		"--permissions", provisioningPermissions)
	if err != nil {
		return "", &provisionError{Op: "roles create " + provisioningRoleName, Output: out, Err: err}
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		return "", &provisionError{Op: "decode roles create JSON", Output: out, Err: err}
	}
	if created.ID == "" {
		return "", &provisionError{Op: "roles create " + provisioningRoleName, Output: out, Err: errNoRoleID}
	}
	return created.ID, nil
}

// runCLI invokes the qrdb CLI over REST against this server's admin port,
// authenticating with the given key pair.
func (s *wireServer) runCLI(cli string, as *qrdbdharness.KeyPair, verb ...string) (string, error) {
	args := []string{
		"--server", "https://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(s.adminPort)),
		"--ca-file", s.ca.CertPath,
		"--cert-file", as.CertPath,
		"--key-file", as.KeyPath,
		"--timeout", provisionTimeout,
	}
	out, err := exec.Command(cli, append(args, verb...)...).CombinedOutput()
	return string(out), err
}

// registerMySQLTLS registers the client TLS configuration the mysql DSN
// references as tls=<name>. go-sql-driver/mysql has no DSN syntax for
// certificate paths, so the config must be handed to it through this
// process-global registry under a name unique to this run.
func (s *wireServer) registerMySQLTLS(tgt target) error {
	cert, err := s.client.TLSCertificate()
	if err != nil {
		return &wireServerError{Op: "load gorm client certificate", Err: err}
	}
	name := "gorm-conformance-" + string(tgt) + "-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	if err := gomysql.RegisterTLSConfig(name, &tls.Config{
		RootCAs:      s.ca.Pool,
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
	}); err != nil {
		return &wireServerError{Op: "register mysql TLS config", Err: err}
	}
	s.mysqlTLSConfigName = name
	return nil
}

// waitForListener blocks until a wire protocol port accepts a TCP connection.
// The admin /health endpoint comes up before the wire listeners bind, so a
// leader-ready node is not by itself evidence that a DSN can be dialled.
func waitForListener(protocol string, port int) error {
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	deadline := time.Now().Add(listenerTimeout)
	var lastErr error
	for {
		conn, err := net.DialTimeout("tcp", addr, dialTimeout)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		lastErr = err
		if !time.Now().Before(deadline) {
			return &listenerTimeoutError{Protocol: protocol, Addr: addr, Timeout: listenerTimeout, LastErr: lastErr}
		}
		time.Sleep(listenerPollInterval)
	}
}
