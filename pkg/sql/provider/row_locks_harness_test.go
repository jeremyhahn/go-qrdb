//go:build qrdb_sql

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

package provider

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	gms "github.com/dolthub/go-mysql-server"
	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/rowlock"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	sqlengine "github.com/jeremyhahn/go-qrdb/pkg/sql/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/internal/sqltest"
)

// lockHarness is a relational database over a REAL in-memory store (see
// sqltest.NewLockingHarness): row locks are made of leases, watches and
// revision order, and a double of those would restate the behaviour under
// test.
type lockHarness struct {
	prov *Provider
	mvcc service.MVCCKVServicer
	eng  *gms.Engine
}

func newLockHarness(t *testing.T, opts ...func(*Provider)) *lockHarness {
	t.Helper()
	h, closeFn, err := sqltest.NewLockingHarness()
	require.NoError(t, err)
	t.Cleanup(closeFn)

	all := append([]func(*Provider){WithRegistry(h.Registry)}, opts...)
	prov := NewProvider("shop", h.KVSvc, all...)
	eng := sqlengine.New(sqlengine.Config{Provider: prov})
	t.Cleanup(func() { _ = eng.Close() })
	return &lockHarness{prov: prov, mvcc: h.MVCCSvc, eng: eng}
}

// lockConn is one client connection: one session, every statement run with
// its own text on the query context, as every SQL surface does.
type lockConn struct {
	t    *testing.T
	eng  *gms.Engine
	sess *QRDBSession
}

func (h *lockHarness) conn(t *testing.T) *lockConn {
	t.Helper()
	c := &lockConn{t: t, eng: h.eng, sess: NewSession(sqle.NewBaseSession(), nil)}
	t.Cleanup(c.sess.SessionEnd)
	return c
}

func (c *lockConn) exec(query string) ([]sqle.Row, error) {
	ctx := sqle.NewContext(context.Background(), sqle.WithSession(c.sess), sqle.WithQuery(query))
	ctx.SetCurrentDatabase("shop")
	_, iter, _, err := c.eng.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	var rows []sqle.Row
	for {
		row, nerr := iter.Next(ctx)
		if errors.Is(nerr, io.EOF) {
			break
		}
		if nerr != nil {
			_ = iter.Close(ctx)
			return nil, nerr
		}
		rows = append(rows, row)
	}
	return rows, iter.Close(ctx)
}

func (c *lockConn) must(query string) []sqle.Row {
	c.t.Helper()
	rows, err := c.exec(query)
	require.NoError(c.t, err, "query %q", query)
	return rows
}

// async runs query on its own goroutine.
func (c *lockConn) async(query string) <-chan error {
	out := make(chan error, 1)
	go func() {
		_, err := c.exec(query)
		out <- err
	}()
	return out
}

// sqlErrorNumber returns the MySQL error number err reaches a client as.
func sqlErrorNumber(err error) int {
	if err == nil {
		return 0
	}
	return sqle.CastSQLError(err).Num
}

// requireSQLError asserts the MySQL number err reaches a client as.
func requireSQLError(t *testing.T, err error, num int) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, num, sqlErrorNumber(err), "error: %v", err)
}

// blockedFor asserts ch delivers nothing within d.
func blockedFor(t *testing.T, ch <-chan error, d time.Duration, what string) {
	t.Helper()
	select {
	case err := <-ch:
		t.Fatalf("%s returned while it should be blocked: %v", what, err)
	case <-time.After(d):
	}
}

// awaitResult waits up to 10s for ch.
func awaitResult(t *testing.T, ch <-chan error, what string) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(10 * time.Second):
		t.Fatalf("%s never returned", what)
		return nil
	}
}

// lockRecords counts the lock records in the store.
func (h *lockHarness) lockRecords(t *testing.T) int {
	t.Helper()
	end := []byte(rowlock.Prefix)
	end[len(end)-1]++
	kvs, _, _, err := h.mvcc.Range(context.Background(), []byte(rowlock.Prefix), end)
	require.NoError(t, err)
	return len(kvs)
}
