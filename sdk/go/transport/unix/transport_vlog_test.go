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

package unix

import (
	"context"
	"testing"

	unixserver "github.com/jeremyhahn/go-qrdb/pkg/api/unix"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/vlogtest"
)

// connectVlogTransport is an SDK Unix client of the REAL pkg/api/unix
// server over an in-memory log service.
func connectVlogTransport(t *testing.T) *Transport {
	t.Helper()
	socketPath := tempSocketPath(t)
	srv := unixserver.NewServer(newMockKVService(), newMockAdminService(), socketPath,
		unixserver.WithLogService(vlogtest.NewService(t)),
		unixserver.WithTLS(testUnixServerTLS),
	)
	if err := srv.Start(); err != nil {
		t.Fatalf("start Unix server: %v", err)
	}
	t.Cleanup(func() { srv.Stop() })
	tr, err := New(transport.WithAddress(socketPath), testClientTLSOption())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := tr.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	return tr
}

func TestTransportVlog_RoundTripOverTheSocket(t *testing.T) {
	vlogtest.Run(t, connectVlogTransport(t))
}

func TestTransportVlog_Guards(t *testing.T) {
	closed := connectVlogTransport(t)
	_ = closed.Close()
	vlogtest.RunGuards(t, connectVlogTransport(t), closed)
}
