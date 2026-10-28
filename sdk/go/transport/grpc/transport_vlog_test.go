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

package grpc

import (
	"net"
	"testing"

	googlegrpc "google.golang.org/grpc"

	grpcpkg "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/vlogtest"
)

// connectVlogTransport is an SDK gRPC client of the REAL QRDBService
// registration over an in-memory log service.
func connectVlogTransport(t *testing.T) *Transport {
	t.Helper()
	srv := googlegrpc.NewServer()
	grpcpkg.RegisterService(srv, nil, nil, grpcpkg.WithRegisterLogService(vlogtest.NewService(t)))
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	tr, cleanup := connectCredentialTransport(t, lis.Addr().String())
	t.Cleanup(cleanup)
	return tr
}

func TestTransportVlog_RoundTripOverTheWire(t *testing.T) {
	vlogtest.Run(t, connectVlogTransport(t))
}

func TestTransportVlog_Guards(t *testing.T) {
	closed := connectVlogTransport(t)
	_ = closed.Close()
	vlogtest.RunGuards(t, connectVlogTransport(t), closed)
}
