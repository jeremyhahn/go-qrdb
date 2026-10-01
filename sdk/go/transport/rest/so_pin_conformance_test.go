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

package rest

import (
	"context"
	"testing"
	"time"

	restserver "github.com/jeremyhahn/go-qrdb/pkg/api/rest"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/sotest"
)

// TestSOPINConformance: an SO rekeys and deletes recovery keys through the
// SDK REST transport against the real REST server with the SO gate on. The
// PIN travels in the X-QRDB-SO-PIN header.
func TestSOPINConformance(t *testing.T) {
	sotest.Run(t, func(t *testing.T, f *sotest.Fixture) sotest.Client {
		t.Helper()
		srv := restserver.NewServer(nil, roundTripAdminStub{}, "127.0.0.1:0",
			restserver.WithTLS(f.ServerTLS), restserver.WithAuthMiddleware(f.MW),
			restserver.WithRoleService(f.Roles), restserver.WithBarrierService(f.Barrier), restserver.WithPINBackend(f.PINs), restserver.WithCeremonyService(f.Ceremony), restserver.WithCeremonyCA(f.CA),
			restserver.WithSOAuthenticator(f.Verifier), restserver.WithSODualFactor())
		if err := srv.Start(); err != nil {
			t.Fatalf("Start: %v", err)
		}
		t.Cleanup(srv.Stop)
		tr, err := New(transport.WithAddress("https://"+srv.Addr()), transport.WithTLSConfig(f.ClientTLS), transport.WithTimeout(5*time.Second))
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if err := tr.Connect(context.Background()); err != nil {
			t.Fatalf("Connect: %v", err)
		}
		t.Cleanup(func() { _ = tr.Close() })
		return tr
	})
}
