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
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/internal/netport"
	"github.com/jeremyhahn/go-qrdb/pkg/clustermig"
	"github.com/jeremyhahn/go-qrdb/pkg/clustertls"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// TestStrconvI pins the allocation-free formatter against strconv.Itoa for
// zero, positive and negative values.
func TestStrconvI(t *testing.T) {
	for _, n := range []int{0, 7, 63002, -1, -42, 1 << 40, -(1 << 40)} {
		assert.Equal(t, strconv.Itoa(n), strconvI(n), "strconvI(%d)", n)
	}
}

// TestDatabaseSQLConfig proves an absent sql block stays absent and a present
// one carries its concurrency mode through unchanged.
func TestDatabaseSQLConfig(t *testing.T) {
	assert.Nil(t, databaseSQLConfig(nil))
	for _, mode := range []string{"", "optimistic", "pessimistic"} {
		got := databaseSQLConfig(&qrdbconfig.DatabaseSQLConfig{Concurrency: mode})
		require.NotNil(t, got, "mode %q", mode)
		assert.Equal(t, database.Concurrency(mode), got.Concurrency)
	}
}

// TestRaftAddrToNodeIDMap proves the map inverts the peer list, and that a
// malformed peer list yields no map rather than a partial one.
func TestRaftAddrToNodeIDMap(t *testing.T) {
	got := raftAddrToNodeIDMap([]string{"1=10.0.0.1:63001", "2=10.0.0.2:63001"})
	assert.Equal(t, map[string]string{"10.0.0.1:63001": "1", "10.0.0.2:63001": "2"}, got)

	for _, peers := range [][]string{{"no-separator"}, {"x=10.0.0.1:63001"}, {"1=10.0.0.1:63001", "=10.0.0.2:63001"}} {
		assert.Nil(t, raftAddrToNodeIDMap(peers), "peers %q", peers)
	}
}

// TestTLSBundleFromMaterial_RefusesBrokenMaterial proves a key that does not
// parse, or a CA that holds no certificate, is refused with a typed
// *TLSGenerateError rather than a bundle that fails at the first handshake.
func TestTLSBundleFromMaterial_RefusesBrokenMaterial(t *testing.T) {
	cases := map[string]func(m *clustertls.Material){
		"key not a key":     func(m *clustertls.Material) { m.KeyPEM = []byte("not a key") },
		"key of another CA": func(m *clustertls.Material) { m.KeyPEM = m.CAKeyPEM },
		"CA not a cert":     func(m *clustertls.Material) { m.CACertPEM = []byte("not a certificate") },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			m, err := clustertls.GenerateCABundle()
			require.NoError(t, err)
			mutate(m)
			bundle, err := tlsBundleFromMaterial(m)
			require.Nil(t, bundle)
			var genErr *TLSGenerateError
			require.ErrorAs(t, err, &genErr)
		})
	}
}

// TestFlagsToEnrollOptions_JoinTokenUnreadable proves a configured join-token
// file that cannot be read fails the enrolment options instead of joining
// with an empty token.
func TestFlagsToEnrollOptions_JoinTokenUnreadable(t *testing.T) {
	f := &startFlags{bootstrapJoinTokenFile: filepath.Join(t.TempDir(), "absent-token")}
	opts, err := flagsToEnrollOptions(f)
	require.Error(t, err)
	assert.Nil(t, opts)
}

// TestResolveTLS_JoinTokenSourceRefused proves a join-token configuration
// that cannot resolve fails TLS resolution on both the cached-certificate
// path (which resumes the enrolment advertiser) and the enrolment path,
// returning no material and starting nothing.
func TestResolveTLS_JoinTokenSourceRefused(t *testing.T) {
	for _, cached := range []bool{true, false} {
		t.Run("cached="+strconv.FormatBool(cached), func(t *testing.T) {
			dir := t.TempDir()
			if cached {
				writeTLSFixtures(t, dir)
			}
			f := &startFlags{
				dataDir:                dir,
				host:                   testBindHost,
				bootstrapJoinTokenFile: filepath.Join(dir, "join-token"),
				bootstrapJoinTokenEnv:  "QRDB_TEST_JOIN_TOKEN",
			}
			bundle, servers, err := resolveTLS(f, testLogger())
			var conflict *qrdbconfig.JoinTokenSourceConflictError
			require.ErrorAs(t, err, &conflict)
			assert.Nil(t, bundle)
			assert.Nil(t, servers)
		})
	}
}

// TestSealSubsystemClose_NothingOpen proves Close is safe on a nil subsystem
// and on one whose seal store was never opened.
func TestSealSubsystemClose_NothingOpen(t *testing.T) {
	var none *sealSubsystem
	assert.NoError(t, none.Close())
	assert.NoError(t, (&sealSubsystem{}).Close())
}

// TestDaemonLocalRecycler_AtTargetWithoutRegistry proves a recycler
// with no barrier registry never claims a scope is already at its target:
// it cannot know, so the migration runs.
func TestDaemonLocalRecycler_AtTargetWithoutRegistry(t *testing.T) {
	r := &daemonLocalRecycler{migrator: &recordingMigrator{}}
	for _, m := range []*clustermig.ClusterMigration{
		{Target: seal.StrategyPassphrase},
		{TenantID: "acme", Target: seal.StrategyPassphrase},
	} {
		assert.False(t, r.AtTarget(m), "scope %q", m.TenantID)
	}
}

// TestDaemonLocalRecycler_AtTargetReadsTenantBarrier proves a tenant-scope
// migration is judged by that tenant's own barrier: a target the tenant
// barrier is not sealed by is not at target, so the migration runs.
func TestDaemonLocalRecycler_AtTargetReadsTenantBarrier(t *testing.T) {
	reg, err := seal.NewBarrierRegistry(slog.New(slog.DiscardHandler), newRecodeSourceBarrier(t))
	require.NoError(t, err)
	tb, err := reg.RegisterTenantDefault("acme")
	require.NoError(t, err)
	require.NotEqual(t, seal.StrategyShamir, tb.Status().Strategy)

	r := &daemonLocalRecycler{migrator: &recordingMigrator{}, barrierReg: reg}
	assert.False(t, r.AtTarget(&clustermig.ClusterMigration{TenantID: "acme", Target: seal.StrategyShamir}))
}

// faultCMKV is the cluster-migration store's KV with a failing scan or
// guarded write (which carries the reap's delete), as a system store that
// has lost quorum fails them.
type faultCMKV struct {
	*cmMemKV
	scanErr  error
	getErr   error
	writeErr error
}

func (k faultCMKV) Get(ctx context.Context, key string) ([]byte, error) {
	if k.getErr != nil {
		return nil, k.getErr
	}
	return k.cmMemKV.Get(ctx, key)
}

func (k faultCMKV) Scan(ctx context.Context, prefix string, fn func(string, []byte) error) error {
	if k.scanErr != nil {
		return k.scanErr
	}
	return k.cmMemKV.Scan(ctx, prefix, fn)
}

func (k faultCMKV) PutAllIfUnchanged(ctx context.Context, guardKey string, guardValue []byte, writes []kvstore.KV) (bool, error) {
	if k.writeErr != nil {
		return false, k.writeErr
	}
	return k.cmMemKV.PutAllIfUnchanged(ctx, guardKey, guardValue, writes)
}

// TestCoordinateMigrations_LeavesRecordsItCannotReap proves the reaper
// removes nothing on a follower, when the migration list cannot be read,
// when a listed record cannot be read to advance it, or when the delete
// itself fails; each such record stays for the next tick.
func TestCoordinateMigrations_LeavesRecordsItCannotReap(t *testing.T) {
	cases := map[string]struct {
		leader bool
		kv     faultCMKV
	}{
		"follower":      {leader: false, kv: faultCMKV{cmMemKV: newCMMemKV()}},
		"list fails":    {leader: true, kv: faultCMKV{cmMemKV: newCMMemKV(), scanErr: errors.New("no quorum")}},
		"delete fails":  {leader: true, kv: faultCMKV{cmMemKV: newCMMemKV(), writeErr: errors.New("no quorum")}},
		"advance fails": {leader: true, kv: faultCMKV{cmMemKV: newCMMemKV(), getErr: errors.New("no quorum")}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			// Seed a completed migration through a healthy view of the same map.
			seed := clustermig.NewStore(tc.kv.cmMemKV)
			seedCoord := clustermig.NewCoordinator(seed, fixedResolver{shard: 1, owners: []string{"n1"}},
				func() bool { return true }, func() int64 { return 1 })
			_, err := seedCoord.Request(ctx, "acme", "", validMigOpts())
			require.NoError(t, err)
			require.NoError(t, seed.PutNodeStatus(ctx, "acme", "", &clustermig.NodeMigrationStatus{NodeID: "n1", Generation: 1, Phase: seal.PhaseComplete}))

			store := clustermig.NewStore(tc.kv)
			coord := clustermig.NewCoordinator(store, fixedResolver{shard: 1, owners: []string{"n1"}},
				func() bool { return tc.leader }, func() int64 { return 1 })
			coordinateMigrations(ctx, coord, store)

			got, err := seed.GetMigration(ctx, "acme", "")
			require.NoError(t, err)
			require.NotNil(t, got, "a record the reaper could not reap was removed")
		})
	}
}

// TestStartRunE_RefusesJoinURLWithoutTLS proves the start command refuses a
// member-side join URL that is not HTTPS before it starts anything.
func TestStartRunE_RefusesJoinURLWithoutTLS(t *testing.T) {
	_, err := executeStart(t,
		"--data-dir", t.TempDir(),
		"--bootstrap-join-url", "http://db.example.com:8443")
	var urlErr *JoinHTTPURLRequiresHTTPSError
	require.ErrorAs(t, err, &urlErr)
	assert.Equal(t, "http://db.example.com:8443", urlErr.URL)
}

// TestStartRunE_AutoPortOnUnassignedHostFails proves a non-static discovery
// start whose bind host this machine does not hold fails at the port probe,
// naming the host, instead of starting on a port it never proved free.
// 192.0.2.1 is TEST-NET-1 (RFC 5737): no bind to it succeeds.
func TestStartRunE_AutoPortOnUnassignedHostFails(t *testing.T) {
	_, err := executeStart(t,
		"--data-dir", t.TempDir(),
		"--discovery", "dns",
		"--dns-domain", "db.example.com",
		"--replica-id", "1",
		"--listen", "0.0.0.0:63001",
		"--raft-address", "127.0.0.1:63001",
		"--host", "192.0.2.1")
	var portErr *netport.NoFreePortError
	require.ErrorAs(t, err, &portErr)
	assert.Equal(t, "192.0.2.1", portErr.Host)
}

// TestWithCredentialBytes_DestroyedSecretFails proves a credential whose
// sealed secret was already released fails the migration step rather than
// running it with no credential.
func TestWithCredentialBytes_DestroyedSecretFails(t *testing.T) {
	ctx := context.Background()
	creds, err := seal.NewCredentials(ctx, []byte("migration passphrase"))
	require.NoError(t, err)
	require.NoError(t, creds.Secret.Destroy())

	called := false
	err = withCredentialBytes(ctx, creds, func([]byte) error { called = true; return nil })
	require.Error(t, err)
	assert.False(t, called, "fn ran without the credential")
}
