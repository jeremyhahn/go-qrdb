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

//go:build qrdb_pgsql

package pgsql

import (
	"context"
	"sync/atomic"
	"testing"

	wire "github.com/jeroenrinzema/psql-wire"
	"github.com/jeroenrinzema/psql-wire/codes"
	psqlerr "github.com/jeroenrinzema/psql-wire/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/internal/sessionguard"
)

// fakeSealedProbe is a deterministic sealedWindowProbe for tests.
type fakeSealedProbe struct {
	sealed atomic.Bool
}

func (p *fakeSealedProbe) IsSealed() bool { return p.sealed.Load() }

// TestSealedSessionHandler_Sealed_RefusesSession verifies new PG sessions
// are refused with SQLSTATE 57P03 while the barrier is sealed.
func TestSealedSessionHandler_Sealed_RefusesSession(t *testing.T) {
	probe := &fakeSealedProbe{}
	probe.sealed.Store(true)

	handler := sealedSessionHandler(probe)
	_, err := handler(context.Background())
	require.Error(t, err)

	var sealedErr *BarrierSealedError
	require.ErrorAs(t, err, &sealedErr)

	desc := psqlerr.Flatten(err)
	assert.Equal(t, codes.CannotConnectNow, desc.Code, "must map to SQLSTATE 57P03 (cannot_connect_now)")
	assert.Equal(t, psqlerr.LevelFatal, desc.Severity)
}

// TestSealedSessionHandler_Unsealed_Passthrough verifies the session gate
// is a passthrough once the barrier is unsealed.
func TestSealedSessionHandler_Unsealed_Passthrough(t *testing.T) {
	probe := &fakeSealedProbe{} // zero value: unsealed
	handler := sealedSessionHandler(probe)
	ctx, err := handler(context.Background())
	require.NoError(t, err)
	assert.NotNil(t, ctx)
}

// TestHandleParse_Sealed_RejectsQuery verifies every query is rejected
// with SQLSTATE 57P03 while the barrier is sealed, before any parsing or
// engine work happens (a nil engine would panic otherwise).
func TestHandleParse_Sealed_RejectsQuery(t *testing.T) {
	probe := &fakeSealedProbe{}
	probe.sealed.Store(true)

	h := NewParseHandler(nil, nil, "qrdb")
	h.sealedProbe = probe

	stmts, err := h.HandleParse(context.Background(), wire.Query{Query: "SELECT 1", SimpleQuery: true})
	require.Nil(t, stmts)
	require.Error(t, err)

	var sealedErr *BarrierSealedError
	require.ErrorAs(t, err, &sealedErr)

	desc := psqlerr.Flatten(err)
	assert.Equal(t, codes.CannotConnectNow, desc.Code)
	assert.Equal(t, psqlerr.LevelError, desc.Severity, "query-level rejection must not be FATAL")
}

// TestHandleParse_ReSeal_EngagesGate verifies the gate engages on a
// seal→unseal→re-seal transition without rebuilding the handler.
func TestHandleParse_ReSeal_EngagesGate(t *testing.T) {
	probe := &fakeSealedProbe{}
	h := NewParseHandler(nil, nil, "qrdb")
	h.sealedProbe = probe

	// Unsealed: an empty query passes the gate and hits the no-op path.
	stmts, err := h.HandleParse(context.Background(), wire.Query{Query: "", SimpleQuery: true})
	require.NoError(t, err)
	require.NotNil(t, stmts)

	probe.sealed.Store(true)
	stmts, err = h.HandleParse(context.Background(), wire.Query{Query: "", SimpleQuery: true})
	require.Nil(t, stmts)
	var sealedErr *BarrierSealedError
	require.ErrorAs(t, err, &sealedErr)
}

// TestWithSealedWindowProbe_Option verifies the option threads the probe
// into the server.
func TestWithSealedWindowProbe_Option(t *testing.T) {
	probe := &fakeSealedProbe{}
	s := NewServer(nil, nil, "127.0.0.1:0", WithSealedWindowProbe(probe))
	assert.NotNil(t, s.sealedProbe)
}

// TestBarrierSealedError_Message verifies the typed error's message
// identifies its origin.
func TestBarrierSealedError_Message(t *testing.T) {
	assert.Equal(t, "sql/pgsql: barrier sealed", (&BarrierSealedError{}).Error())
}

// stateSealedProbe reports a seal state through sealedwindow.StateProbe.
type stateSealedProbe struct{ sealed, unconfirmed bool }

func (p stateSealedProbe) IsSealed() bool                        { return p.sealed }
func (p stateSealedProbe) SealState() (sealed, unconfirmed bool) { return p.sealed, p.unconfirmed }

// TestSealedSessionHandler_Unconfirmed_IsNotSealed: a node that cannot
// confirm the cluster seal state refuses the session as
// SealStateUnconfirmedError under 57P03, never as BarrierSealedError.
func TestSealedSessionHandler_Unconfirmed_IsNotSealed(t *testing.T) {
	_, err := sealedSessionHandler(stateSealedProbe{sealed: true, unconfirmed: true})(context.Background())
	var unconfirmed *SealStateUnconfirmedError
	require.ErrorAs(t, err, &unconfirmed)
	var sealedErr *BarrierSealedError
	assert.NotErrorAs(t, err, &sealedErr)
	var cause *clusterseal.SealStateUnconfirmedError
	assert.ErrorAs(t, err, &cause, "the wire error unwraps to the clusterseal cause")
	desc := psqlerr.Flatten(err)
	assert.Equal(t, codes.CannotConnectNow, desc.Code)
	assert.Equal(t, psqlerr.LevelFatal, desc.Severity)
}

// TestHandleParse_Unconfirmed_IsNotSealed: the query gate reports the
// unconfirmed state with its own error at ERROR severity, and a sealed
// state probe still reports BarrierSealedError.
func TestHandleParse_Unconfirmed_IsNotSealed(t *testing.T) {
	h := NewParseHandler(nil, nil, "qrdb")
	h.sealedProbe = stateSealedProbe{sealed: true, unconfirmed: true}
	_, err := h.HandleParse(context.Background(), wire.Query{Query: "SELECT 1", SimpleQuery: true})
	var unconfirmed *SealStateUnconfirmedError
	require.ErrorAs(t, err, &unconfirmed)
	assert.Equal(t, psqlerr.LevelError, psqlerr.Flatten(err).Severity)

	h.sealedProbe = stateSealedProbe{sealed: true}
	_, err = h.HandleParse(context.Background(), wire.Query{Query: "SELECT 1", SimpleQuery: true})
	var sealedErr *BarrierSealedError
	require.ErrorAs(t, err, &sealedErr)
	assert.NotErrorAs(t, err, &unconfirmed)
}

// TestWireRefusal: each sessionguard refusal maps to its typed wire error
// under 57P03; any other error, and nil, pass through untouched.
func TestWireRefusal(t *testing.T) {
	other := &BarrierSealedError{}
	assert.NoError(t, wireRefusal(nil, psqlerr.LevelError))
	assert.Same(t, error(other), wireRefusal(other, psqlerr.LevelError), "an unrelated error passes through")

	err := wireRefusal(&sessionguard.SealedError{}, psqlerr.LevelFatal)
	var sealedErr *BarrierSealedError
	require.ErrorAs(t, err, &sealedErr)
	assert.Equal(t, codes.CannotConnectNow, psqlerr.Flatten(err).Code)

	err = wireRefusal(&clusterseal.SealStateUnconfirmedError{}, psqlerr.LevelError)
	var unconfirmed *SealStateUnconfirmedError
	require.ErrorAs(t, err, &unconfirmed)
	assert.Equal(t, codes.CannotConnectNow, psqlerr.Flatten(err).Code)
}

// TestSealStateUnconfirmedError_Message: the typed error names its origin
// and says the caller should retry.
func TestSealStateUnconfirmedError_Message(t *testing.T) {
	assert.Equal(t, "sql/pgsql: seal state unconfirmed: this node cannot confirm the cluster seal state; retry", (&SealStateUnconfirmedError{}).Error())
	var cause *clusterseal.SealStateUnconfirmedError
	assert.ErrorAs(t, &SealStateUnconfirmedError{}, &cause)
}
