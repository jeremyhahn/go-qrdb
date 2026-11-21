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

//go:build qrdb_mysql

package mysql

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	gomysql "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/internal/sessionguard"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/internal/sqltest"
)

// fakeSealedProbe is a deterministic sealedWindowProbe for tests.
type fakeSealedProbe struct {
	sealed atomic.Bool
}

func (p *fakeSealedProbe) IsSealed() bool { return p.sealed.Load() }

// TestSessionBuilder_Sealed_RefusesConnection verifies that new MySQL
// sessions are refused with BarrierSealedError while the barrier is
// sealed (fail-closed), before any authentication work happens.
func TestSessionBuilder_Sealed_RefusesConnection(t *testing.T) {
	probe := &fakeSealedProbe{}
	probe.sealed.Store(true)
	sb := newSessionBuilder(nil, probe)

	conn := newPlainConn()
	defer func() { _ = conn.Conn.Close() }()

	sess, err := sb.Build(context.Background(), conn, "127.0.0.1:3306")
	require.Nil(t, sess)
	var sealedErr *BarrierSealedError
	require.ErrorAs(t, err, &sealedErr)
}

// TestSessionBuilder_Unsealed_Passthrough verifies the gate is a
// passthrough once the barrier is unsealed and re-engages on re-seal.
func TestSessionBuilder_Unsealed_Passthrough(t *testing.T) {
	probe := &fakeSealedProbe{} // zero value: unsealed
	sb := newSessionBuilder(nil, probe)

	conn := newPlainConn()
	defer func() { _ = conn.Conn.Close() }()

	sess, err := sb.Build(context.Background(), conn, "127.0.0.1:3306")
	require.NoError(t, err)
	require.NotNil(t, sess)

	// Re-seal: the gate must engage again for new sessions.
	probe.sealed.Store(true)
	conn2 := newPlainConn()
	defer func() { _ = conn2.Conn.Close() }()
	sess2, err2 := sb.Build(context.Background(), conn2, "127.0.0.1:3306")
	require.Nil(t, sess2)
	var sealedErr *BarrierSealedError
	require.ErrorAs(t, err2, &sealedErr)
}

// TestBarrierSealedError_Message verifies the typed error's message
// identifies its origin.
func TestBarrierSealedError_Message(t *testing.T) {
	assert.Equal(t, "sql/mysql: barrier sealed", (&BarrierSealedError{}).Error())
}

// stateSealedProbe reports a seal state through sealedwindow.StateProbe.
type stateSealedProbe struct{ sealed, unconfirmed bool }

func (p stateSealedProbe) IsSealed() bool                        { return p.sealed }
func (p stateSealedProbe) SealState() (sealed, unconfirmed bool) { return p.sealed, p.unconfirmed }

// TestSessionBuilder_Unconfirmed_IsNotSealed: a node that cannot confirm
// the cluster seal state refuses the connection with SealStateUnconfirmedError,
// never BarrierSealedError; a sealed barrier still reports BarrierSealedError.
func TestSessionBuilder_Unconfirmed_IsNotSealed(t *testing.T) {
	for name, tc := range map[string]struct {
		probe       stateSealedProbe
		unconfirmed bool
	}{
		"seal state unconfirmed": {probe: stateSealedProbe{sealed: true, unconfirmed: true}, unconfirmed: true},
		"barrier sealed":         {probe: stateSealedProbe{sealed: true}},
	} {
		t.Run(name, func(t *testing.T) {
			conn := newPlainConn()
			defer func() { _ = conn.Conn.Close() }()
			sess, err := newSessionBuilder(nil, tc.probe).Build(context.Background(), conn, "127.0.0.1:3306")
			require.Nil(t, sess)
			_, isUnconfirmed := errors.AsType[*SealStateUnconfirmedError](err)
			_, isSealed := errors.AsType[*BarrierSealedError](err)
			assert.Equal(t, tc.unconfirmed, isUnconfirmed, "err = %v", err)
			assert.Equal(t, !tc.unconfirmed, isSealed, "err = %v", err)
		})
	}
}

// TestWireRefusal: each sessionguard refusal maps to its typed MySQL error;
// any other error, and nil, pass through untouched.
func TestWireRefusal(t *testing.T) {
	other := &QueryTimeoutError{}
	assert.NoError(t, wireRefusal(nil))
	assert.Same(t, error(other), wireRefusal(other))
	var sealedErr *BarrierSealedError
	assert.ErrorAs(t, wireRefusal(&sessionguard.SealedError{}), &sealedErr)
	var unconfirmed *SealStateUnconfirmedError
	assert.ErrorAs(t, wireRefusal(&clusterseal.SealStateUnconfirmedError{}), &unconfirmed)
	assert.NoError(t, probeRefusal(stateSealedProbe{}))
	assert.ErrorAs(t, probeRefusal(stateSealedProbe{sealed: true, unconfirmed: true}), &unconfirmed)
}

// TestSealStateUnconfirmedError_Message: the typed error names its origin,
// says the caller should retry, and unwraps to the clusterseal cause.
func TestSealStateUnconfirmedError_Message(t *testing.T) {
	assert.Equal(t, "sql/mysql: seal state unconfirmed: this node cannot confirm the cluster seal state; retry", (&SealStateUnconfirmedError{}).Error())
	var cause *clusterseal.SealStateUnconfirmedError
	assert.ErrorAs(t, &SealStateUnconfirmedError{}, &cause)
}

// TestOpenSession_RefusedOnItsNextQueryOnceSealed pins the per-query half of
// the sealed-window gate, as pgsql has it: a session established while the
// node served is refused on its very next query once the node stops serving
// -- its barrier sealed, or a cluster seal holding it, cutting it off or not
// yet confirmed -- for a plain query and a prepared statement alike, and it
// serves again once the node does.
func TestOpenSession_RefusedOnItsNextQueryOnceSealed(t *testing.T) {
	h, closeFn, err := sqltest.NewLockingHarness()
	require.NoError(t, err)
	t.Cleanup(closeFn)
	probe := &fakeSealedProbe{}
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0",
		WithDatabaseName("qrdb"),
		WithServiceRegistry(h.Registry),
		WithSealedWindowProbe(probe),
		WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
	)
	require.NoError(t, srv.Start())
	t.Cleanup(srv.Stop)

	cfg := gomysql.NewConfig()
	cfg.Net = "tcp"
	cfg.Addr = srv.Addr()
	cfg.User = "qrdb"
	cfg.DBName = "qrdb"
	cfg.AllowNativePasswords = true
	cfg.Timeout = 30 * time.Second
	db, err := sql.Open("mysql", cfg.FormatDSN())
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	_, err = conn.ExecContext(ctx, "CREATE TABLE t (id BIGINT PRIMARY KEY)")
	require.NoError(t, err)
	stmt, err := conn.PrepareContext(ctx, "SELECT id FROM t WHERE id = ?")
	require.NoError(t, err)
	t.Cleanup(func() { _ = stmt.Close() })

	probe.sealed.Store(true)
	_, err = conn.ExecContext(ctx, "INSERT INTO t VALUES (1)")
	require.ErrorContains(t, err, (&BarrierSealedError{}).Error(), "a query on an open session ran while the node was sealed")
	rows, err := stmt.QueryContext(ctx, 1)
	if err == nil {
		_ = rows.Close()
	}
	require.ErrorContains(t, err, (&BarrierSealedError{}).Error(), "a prepared statement on an open session ran while the node was sealed")

	probe.sealed.Store(false)
	_, err = conn.ExecContext(ctx, "INSERT INTO t VALUES (1)")
	require.NoError(t, err, "the session did not serve again once the node did")
}
