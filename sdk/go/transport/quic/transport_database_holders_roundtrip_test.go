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
	"testing"
	"time"

	quicserver "github.com/jeremyhahn/go-qrdb/pkg/api/quic"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/dbholdertest"
)

// roundTripAdmin answers the health check the client's Connect makes.
type roundTripAdmin struct{ service.AdminServicer }

func (roundTripAdmin) Health(context.Context) (*service.HealthStatus, error) {
	return &service.HealthStatus{Status: "ok"}, nil
}

// TestQUICRoundTrip_DatabaseHolders runs the database share-holder suite
// through the SDK QUIC client against the REAL pkg/api/quic server over a
// real database service. The client presents no certificate, so its claim
// is refused.
func TestQUICRoundTrip_DatabaseHolders(t *testing.T) {
	fx := dbholdertest.NewFixture(t)
	serverCfg, clientCfg := e2eTLS(t)
	srv := quicserver.NewServer(nil, roundTripAdmin{}, "127.0.0.1:0",
		quicserver.WithTLS(serverCfg),
		quicserver.WithDatabaseService(fx.Service),
	)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)

	tr, err := New(transport.WithAddress(srv.Addr()), transport.WithTLSConfig(clientCfg), transport.WithTimeout(dbholdertest.RequestTimeout))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := tr.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })

	dbholdertest.Run(t, tr, fx, dbholdertest.ClaimRefusedWithStatus(int(wire.StatusUnauthenticated)))
}
