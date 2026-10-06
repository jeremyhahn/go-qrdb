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

package quic

import (
	"context"
	"errors"
	"testing"
	"time"

	quicgo "github.com/quic-go/quic-go"

	"github.com/jeremyhahn/go-qrdb/pkg/api/connlimit"
)

// TestWithMaxConnsPerPeer_SetsField mirrors the equivalent test in
// pkg/api/grpc: the option only needs to reach the field here, since
// resolveMaxConnsPerPeer/Start's behavior is covered separately.
func TestWithMaxConnsPerPeer_SetsField(t *testing.T) {
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0",
		WithMaxConnsPerPeer(7))
	if srv.maxConnsPerPeer != 7 {
		t.Fatalf("expected maxConnsPerPeer 7, got %d", srv.maxConnsPerPeer)
	}
}

// TestServer_Start_MaxConnsPerPeerZero_LeavesRawListener confirms Start does
// not pay for a connlimit wrapper it was not asked for: with no
// WithMaxConnsPerPeer option (the zero value), the listener Start installs
// is the raw *quic.Listener quic-go returned, not a connlimit.QUICListener.
func TestServer_Start_MaxConnsPerPeerZero_LeavesRawListener(t *testing.T) {
	bundle := generateIntegrationTLS(t)
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0",
		WithTLS(bundle.serverCfg))
	if err := srv.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer srv.Stop()

	if _, wrapped := srv.listener.(*connlimit.QUICListener); wrapped {
		t.Fatal("expected the raw quic-go listener, not a connlimit.QUICListener, when maxConnsPerPeer is unset")
	}
}

// TestIntegration_MaxConnsPerPeer_RefusesOverCapConnection is an
// end-to-end check of go-qrdb#576's QUIC per-peer cap: with
// WithMaxConnsPerPeer(1), a second connection from the same client while
// the first is still open is closed with connlimit.QUICOverCapErrorCode
// before any stream on it can be used, and the first connection is
// unaffected.
func TestIntegration_MaxConnsPerPeer_RefusesOverCapConnection(t *testing.T) {
	env := startIntegrationServer(t, &mockKVService{}, &mockAdminService{},
		WithMaxConnsPerPeer(1),
	)

	if _, wrapped := env.srv.listener.(*connlimit.QUICListener); !wrapped {
		t.Fatal("expected the listener to be wrapped in a connlimit.QUICListener when maxConnsPerPeer is set")
	}

	first := env.dialServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	second, err := quicgo.DialAddr(ctx, env.srv.Addr(), env.clientCfg, &quicgo.Config{
		MaxIdleTimeout: 5 * time.Second,
	})
	cancel()
	if err != nil {
		// quic-go's handshake can itself observe the refusal as a dial
		// error rather than a connection that then closes; either is an
		// acceptable manifestation of "refused", so this path is not a
		// failure on its own.
		return
	}
	t.Cleanup(func() { _ = second.CloseWithError(0, "test done") })

	select {
	case <-second.Context().Done():
	case <-time.After(3 * time.Second):
		t.Fatal("over-cap connection was not refused within timeout")
	}

	var appErr *quicgo.ApplicationError
	closeErr := context.Cause(second.Context())
	if !errors.As(closeErr, &appErr) {
		t.Fatalf("expected an ApplicationError closing the refused connection, got %v (%T)", closeErr, closeErr)
	}
	if appErr.ErrorCode != connlimit.QUICOverCapErrorCode {
		t.Fatalf("expected error code %d, got %d", connlimit.QUICOverCapErrorCode, appErr.ErrorCode)
	}

	// The first connection, under the cap, is unaffected.
	select {
	case <-first.Context().Done():
		t.Fatal("first connection should remain open")
	default:
	}
}
