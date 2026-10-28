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

package unix

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
)

// headOnlyLogService answers Head; the Unix tests call nothing else, and the
// embedded nil interface makes any other call fail loudly.
type headOnlyLogService struct {
	service.LogService
	err error
}

func (s *headOnlyLogService) Head(_ context.Context, req *service.HeadRequest) (*service.HeadResponse, error) {
	if s.err != nil {
		return nil, s.err
	}
	return &service.HeadResponse{Head: vlog.Head{Size: 3, Root: []byte(req.Log)}}, nil
}

// vlogHeadOverSocket starts a Unix server with opts and calls VlogHead on it.
func vlogHeadOverSocket(t *testing.T, opts ...ServerOption) (*pb.VlogHeadResponse, error) {
	t.Helper()
	socketPath := tempSocketPath(t)
	tlsCfg := selfSignedTLSConfig(t)
	srv := NewServer(newMockKVService(), &mockAdminService{}, socketPath, append(opts, WithTLS(tlsCfg))...)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	t.Cleanup(srv.Stop)

	conn := dialUnixTLS(t, socketPath, tlsCfg)
	t.Cleanup(func() { _ = conn.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return pb.NewQRDBServiceClient(conn).VlogHead(ctx, &pb.VlogHeadRequest{Log: "audit"})
}

// TestWithLogService_ServesTheVlogMethods checks the Unix socket serves the
// verifiable-log methods of the shared gRPC service once a LogService is set.
func TestWithLogService_ServesTheVlogMethods(t *testing.T) {
	resp, err := vlogHeadOverSocket(t, WithLogService(&headOnlyLogService{}))
	if err != nil {
		t.Fatalf("VlogHead: %v", err)
	}
	if resp.GetSize() != 3 || string(resp.GetRoot()) != "audit" {
		t.Fatalf("head = %+v", resp)
	}
}

// TestWithLogService_MapsTheServiceError checks a typed vlog refusal crosses
// the socket as its class's code.
func TestWithLogService_MapsTheServiceError(t *testing.T) {
	_, err := vlogHeadOverSocket(t, WithLogService(&headOnlyLogService{err: &vlog.LogNotFoundError{}}))
	if status.Code(err) != codes.NotFound {
		t.Fatalf("err = %v, want NotFound", err)
	}
}

// TestWithLogService_Unset checks a socket with no LogService refuses the
// methods rather than serving them.
func TestWithLogService_Unset(t *testing.T) {
	srv := NewServer(newMockKVService(), &mockAdminService{}, "/tmp/test.sock", WithLogService(nil))
	if srv.logSvc != nil {
		t.Fatal("expected nil logSvc")
	}
	if _, err := vlogHeadOverSocket(t); status.Code(err) == codes.OK {
		t.Fatal("VlogHead without a LogService succeeded")
	}
}
