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

//go:build qrdb_vlog

package quic

import (
	"context"
	"testing"
	"time"

	quicserver "github.com/jeremyhahn/go-qrdb/pkg/api/quic"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/vlogtest"
)

// connectVlogTransport is an SDK QUIC client of the REAL pkg/api/quic
// server over an in-memory log service.
func connectVlogTransport(t *testing.T) *Transport {
	t.Helper()
	serverCfg, clientCfg := e2eTLS(t)
	srv := quicserver.NewServer(nil, roundTripAdmin{}, "127.0.0.1:0",
		quicserver.WithTLS(serverCfg),
		quicserver.WithLogService(vlogtest.NewService(t)),
	)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)
	tr, err := New(transport.WithAddress(srv.Addr()), transport.WithTLSConfig(clientCfg))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := tr.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	return tr
}

func TestQUICVlog_RoundTripOverTheWire(t *testing.T) {
	vlogtest.Run(t, connectVlogTransport(t))
}

func TestQUICVlog_Guards(t *testing.T) {
	closed := connectVlogTransport(t)
	_ = closed.Close()
	vlogtest.RunGuards(t, connectVlogTransport(t), closed)
}
