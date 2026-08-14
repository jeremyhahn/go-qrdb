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

	"github.com/jeroenrinzema/psql-wire/codes"
	psqlerr "github.com/jeroenrinzema/psql-wire/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

	stmts, err := h.HandleParse(context.Background(), "SELECT 1")
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
	stmts, err := h.HandleParse(context.Background(), "")
	require.NoError(t, err)
	require.NotNil(t, stmts)

	probe.sealed.Store(true)
	stmts, err = h.HandleParse(context.Background(), "")
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
