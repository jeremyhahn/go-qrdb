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
	"testing"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

func TestNewSession(t *testing.T) {
	base := sqle.NewBaseSession()
	identity := &auth.Identity{
		Subject:  "alice",
		TenantID: "acme",
		Roles:    []string{"admin"},
	}

	sess := NewSession(base, identity)

	require.NotNil(t, sess)
	assert.Equal(t, identity, sess.Identity())
	assert.Equal(t, "acme", sess.TenantID())
}

func TestNewSession_NilIdentity(t *testing.T) {
	base := sqle.NewBaseSession()

	sess := NewSession(base, nil)

	require.NotNil(t, sess)
	assert.Nil(t, sess.Identity())
	assert.Equal(t, "system", sess.TenantID())
}

func TestQRDBSession_TenantID_EmptyTenant(t *testing.T) {
	base := sqle.NewBaseSession()
	identity := &auth.Identity{
		Subject:  "bob",
		TenantID: "",
	}

	sess := NewSession(base, identity)

	assert.Equal(t, "system", sess.TenantID())
}

func TestQRDBSession_TenantID_NonEmptyTenant(t *testing.T) {
	base := sqle.NewBaseSession()
	identity := &auth.Identity{
		Subject:  "charlie",
		TenantID: "org-123",
	}

	sess := NewSession(base, identity)

	assert.Equal(t, "org-123", sess.TenantID())
}

func TestQRDBSession_Identity_ReturnsCorrectValue(t *testing.T) {
	base := sqle.NewBaseSession()
	identity := &auth.Identity{
		Subject:  "dave",
		TenantID: "corp",
		Roles:    []string{"reader", "writer"},
		Metadata: map[string]string{"env": "prod"},
	}

	sess := NewSession(base, identity)

	got := sess.Identity()
	assert.Equal(t, "dave", got.Subject)
	assert.Equal(t, "corp", got.TenantID)
	assert.Equal(t, []string{"reader", "writer"}, got.Roles)
	assert.Equal(t, "prod", got.Metadata["env"])
}

func TestQRDBSession_EmbeddedBaseSession(t *testing.T) {
	base := sqle.NewBaseSession()
	identity := &auth.Identity{Subject: "eve"}

	sess := NewSession(base, identity)

	// Verify the embedded BaseSession is functional by calling its methods.
	assert.Equal(t, base.ID(), sess.ID())
}

// TestQRDBSession_SharesBaseSessionState verifies that the session is embedded
// by pointer, so mutable per-connection state is shared with the base session
// rather than copied at construction. Transaction state lives on
// sql.BaseSession, so a copy would silently strand a transaction started
// through the QRDBSession where no other holder of the base session could see
// it.
func TestQRDBSession_SharesBaseSessionState(t *testing.T) {
	base := sqle.NewBaseSession()
	sess := NewSession(base, &auth.Identity{Subject: "frank"})

	// A write through the wrapper is observable on the base session.
	sess.SetCurrentDatabase("via_wrapper")
	assert.Equal(t, "via_wrapper", base.GetCurrentDatabase())

	// ...and a write through the base session is observable on the wrapper.
	base.SetCurrentDatabase("via_base")
	assert.Equal(t, "via_base", sess.GetCurrentDatabase())
}

// stubTransaction is a minimal sql.Transaction used to assert that
// transaction state set through a QRDBSession lands on the shared base
// session rather than on a copy.
type stubTransaction struct{ name string }

func (t stubTransaction) String() string   { return t.name }
func (t stubTransaction) IsReadOnly() bool { return false }

// TestQRDBSession_TransactionStateIsShared verifies that a transaction set
// through the QRDBSession is visible on the underlying base session. This is
// the property that lets go-mysql-server's sql.TransactionSession contract
// work across the statements of one connection: GMS reads and writes the
// active transaction through sql.BaseSession, so a by-value copy would strand
// it.
func TestQRDBSession_TransactionStateIsShared(t *testing.T) {
	base := sqle.NewBaseSession()
	sess := NewSession(base, nil)

	require.Nil(t, sess.GetTransaction())

	tx := stubTransaction{name: "txn-1"}
	sess.SetTransaction(tx)

	require.NotNil(t, base.GetTransaction())
	assert.Equal(t, "txn-1", base.GetTransaction().String(),
		"transaction set through the wrapper must be visible on the base session")
	assert.Equal(t, tx, sess.GetTransaction())
}
