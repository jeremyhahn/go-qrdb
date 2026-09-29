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

package quic

import (
	"context"
	"errors"
	"testing"
	"time"

	quicserver "github.com/jeremyhahn/go-qrdb/pkg/api/quic"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/sotest"
)

// soConformanceAdmin answers Health, which the SDK's Connect checks.
type soConformanceAdmin struct{ service.AdminServicer }

func (soConformanceAdmin) Health(context.Context) (*service.HealthStatus, error) {
	return &service.HealthStatus{Status: "ok"}, nil
}

func startSOConformance(t *testing.T, f *sotest.Fixture) *Transport {
	t.Helper()
	srv := quicserver.NewServer(nil, soConformanceAdmin{}, "127.0.0.1:0",
		quicserver.WithTLS(f.ServerTLS), quicserver.WithAuthMiddleware(f.MW),
		quicserver.WithBarrierService(f.Barrier), quicserver.WithPINBackend(f.PINs), quicserver.WithCeremonyService(f.Ceremony), quicserver.WithCeremonyCA(f.CA), quicserver.WithSOGate(f.Gate))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)
	tr, err := New(transport.WithAddress(srv.Addr()), transport.WithTLSConfig(f.ClientTLS), transport.WithTimeout(5*time.Second))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := tr.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	return tr
}

// TestSOPINConformance: an SO rekeys and deletes recovery keys through the
// SDK QUIC transport against the real QUIC server with the SO gate on. The
// PIN travels in the request message's so_pin field.
func TestSOPINConformance(t *testing.T) {
	sotest.Run(t, func(t *testing.T, f *sotest.Fixture) sotest.Client {
		return startSOConformance(t, f)
	})
}

// TestSOPINConformance_ConflictingPINsAreRefused: QUIC carries one PIN, so a
// request SOPin that differs from the bound PIN is refused before sending.
func TestSOPINConformance_ConflictingPINsAreRefused(t *testing.T) {
	f := sotest.NewFixture(t)
	tr := startSOConformance(t, f)
	ctx, release := sotest.BindPIN(t, sotest.PIN)
	defer release()
	_, err := tr.BarrierRekeyStart(ctx, &transport.RekeyStartRequest{Domain: transport.DomainUnseal, Threshold: 2, SOPin: "other"})
	var conflict *transport.SOPINConflictError
	if !errors.As(err, &conflict) || conflict.Method != "BarrierRekey" {
		t.Fatalf("err = %v, want *transport.SOPINConflictError", err)
	}
	if f.Barrier.Rekeys() != 0 {
		t.Fatal("a refused rekey reached the service")
	}
	// The same PIN in both places is not a conflict.
	if _, err := tr.BarrierRekeyStart(ctx, &transport.RekeyStartRequest{Domain: transport.DomainUnseal, Threshold: 2, SOPin: sotest.PIN}); err != nil {
		t.Fatalf("matching PINs: %v", err)
	}
}
