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

package unix

import (
	"context"
	"testing"
	"time"

	unixserver "github.com/jeremyhahn/go-qrdb/pkg/api/unix"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/sotest"
)

// TestSOPINConformance: an SO rekeys and deletes recovery keys through the
// SDK Unix socket transport against the real Unix server with the SO gate on.
// The PIN travels in the x-qrdb-so-pin metadata entry.
func TestSOPINConformance(t *testing.T) {
	sotest.Run(t, func(t *testing.T, f *sotest.Fixture) sotest.Client {
		t.Helper()
		socketPath := tempSocketPath(t)
		srv := unixserver.NewServer(newMockKVService(), newMockAdminService(), socketPath,
			unixserver.WithTLS(f.ServerTLS), unixserver.WithAuthMiddleware(f.MW),
			unixserver.WithBarrierService(f.Barrier), unixserver.WithPINBackend(f.PINs), unixserver.WithCeremonyService(f.Ceremony), unixserver.WithCeremonyCA(f.CA), unixserver.WithSOGate(f.Gate))
		if err := srv.Start(); err != nil {
			t.Fatalf("Start: %v", err)
		}
		t.Cleanup(srv.Stop)
		tr, err := New(transport.WithAddress(socketPath), transport.WithTLSConfig(f.ClientTLS), transport.WithTimeout(5*time.Second))
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
