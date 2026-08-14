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
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	assert.Equal(t, "sql/mysql: barrier sealed", (&BarrierSealedError{}).Error())
}
