//go:build qrdb_sql

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

package provider

import (
	"context"
	"errors"
	"testing"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

// switchAdmitter admits until refuse is set, recording each identity it was
// asked about.
type switchAdmitter struct {
	refuse error
	asked  []*auth.Identity
}

func (a *switchAdmitter) AdmitIdentity(_ context.Context, id *auth.Identity) error {
	a.asked = append(a.asked, id)
	return a.refuse
}

var errTenantSuspended = errors.New("tenant suspended")

// A session's identity is re-admitted before every command: once refused --
// its tenant suspended after the session opened -- the next command fails
// with the admitter's own error, and nothing of the command is opened.
func TestSessionCommandBegin_ReadmitsTheIdentityEachCommand(t *testing.T) {
	id := &auth.Identity{Subject: "bob", TenantID: "acme"}
	admitter := &switchAdmitter{}
	sess := NewSession(sqle.NewBaseSession(), id, WithSessionAdmitter(admitter))

	require.NoError(t, sess.CommandBegin())
	sess.CommandEnd()
	scope := sess.stmt.Load()

	admitter.refuse = errTenantSuspended
	require.ErrorIs(t, sess.CommandBegin(), errTenantSuspended)
	assert.False(t, sess.commandOpen.Load(), "a refused command opens nothing")
	assert.Same(t, scope, sess.stmt.Load(), "a refused command opens no resolution scope")

	admitter.refuse = nil
	require.NoError(t, sess.CommandBegin(), "admitted again once the admitter admits")
	sess.CommandEnd()

	require.Len(t, admitter.asked, 3)
	for _, asked := range admitter.asked {
		assert.Same(t, id, asked, "the admitter is asked about the session's own identity")
	}
}

// A session built without an admitter admits every command, as a
// request-scoped session the chain already authorized does.
func TestSessionCommandBegin_NoAdmitterAdmits(t *testing.T) {
	sess := NewSession(sqle.NewBaseSession(), &auth.Identity{Subject: "bob", TenantID: "acme"})
	require.NoError(t, sess.CommandBegin())
	sess.CommandEnd()
	require.NoError(t, sess.CommandBegin())
	sess.CommandEnd()
}

// Admit asks the session's admitter about its identity outside any command --
// it opens none, so a refusal needs no CommandEnd -- and returns its refusal.
func TestSessionAdmit_AsksTheAdmitterOutsideACommand(t *testing.T) {
	id := &auth.Identity{Subject: "alice", TenantID: "acme"}
	adm := &switchAdmitter{}
	sess := NewSession(sqle.NewBaseSession(), id, WithSessionAdmitter(adm))

	require.NoError(t, sess.Admit(context.Background()))
	adm.refuse = errTenantSuspended
	require.ErrorIs(t, sess.Admit(context.Background()), errTenantSuspended)
	require.Equal(t, []*auth.Identity{id, id}, adm.asked)

	adm.refuse = nil
	require.NoError(t, sess.CommandBegin(), "Admit left a command open")
	sess.CommandEnd()
}

// A session with no admitter admits every Admit.
func TestSessionAdmit_NoAdmitterAdmits(t *testing.T) {
	sess := NewSession(sqle.NewBaseSession(), &auth.Identity{Subject: "bob", TenantID: "acme"})
	require.NoError(t, sess.Admit(context.Background()))
}
