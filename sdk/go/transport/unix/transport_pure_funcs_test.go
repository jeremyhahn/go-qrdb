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

// Pure-function unit tests for transport helper functions that do not need a
// live gRPC server: protoConsistency, protoWriteMode, mapGRPCError, and the
// Request method error path via a disconnected transport.

package unix

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ===========================================================================
// protoConsistency
// ===========================================================================

func TestProtoConsistency_Unspecified(t *testing.T) {
	got := protoConsistency()
	if got != pb.ConsistencyLevel_CONSISTENCY_LEVEL_UNSPECIFIED {
		t.Errorf("expected UNSPECIFIED, got %v", got)
	}
}

func TestProtoConsistency_Strong(t *testing.T) {
	got := protoConsistency(transport.WithConsistency(transport.ConsistencyStrong))
	if got != pb.ConsistencyLevel_CONSISTENCY_LEVEL_STRONG {
		t.Errorf("expected STRONG, got %v", got)
	}
}

func TestProtoConsistency_Linearizable(t *testing.T) {
	got := protoConsistency(transport.WithConsistency(transport.ConsistencyLinearizable))
	if got != pb.ConsistencyLevel_CONSISTENCY_LEVEL_LINEARIZABLE {
		t.Errorf("expected LINEARIZABLE, got %v", got)
	}
}

func TestProtoConsistency_Eventual(t *testing.T) {
	got := protoConsistency(transport.WithConsistency(transport.ConsistencyEventual))
	if got != pb.ConsistencyLevel_CONSISTENCY_LEVEL_EVENTUAL {
		t.Errorf("expected EVENTUAL, got %v", got)
	}
}

func TestProtoConsistency_AllowStale(t *testing.T) {
	// AllowStale without an explicit level should map to EVENTUAL.
	got := protoConsistency(transport.WithAllowStale())
	if got != pb.ConsistencyLevel_CONSISTENCY_LEVEL_EVENTUAL {
		t.Errorf("expected EVENTUAL for AllowStale, got %v", got)
	}
}

// ===========================================================================
// protoWriteMode
// ===========================================================================

func TestProtoWriteMode_Unspecified(t *testing.T) {
	got := protoWriteMode()
	if got != pb.WriteMode_WRITE_MODE_UNSPECIFIED {
		t.Errorf("expected UNSPECIFIED, got %v", got)
	}
}

func TestProtoWriteMode_Sync(t *testing.T) {
	got := protoWriteMode(transport.WithWriteMode(transport.WriteModeSync))
	if got != pb.WriteMode_WRITE_MODE_SYNC {
		t.Errorf("expected SYNC, got %v", got)
	}
}

func TestProtoWriteMode_WALDurable(t *testing.T) {
	got := protoWriteMode(transport.WithWriteMode(transport.WriteModeWALDurable))
	if got != pb.WriteMode_WRITE_MODE_WAL_DURABLE {
		t.Errorf("expected WAL_DURABLE, got %v", got)
	}
}

func TestProtoWriteMode_Batched(t *testing.T) {
	got := protoWriteMode(transport.WithWriteMode(transport.WriteModeBatched))
	if got != pb.WriteMode_WRITE_MODE_BATCHED {
		t.Errorf("expected BATCHED, got %v", got)
	}
}

// ===========================================================================
// mapGRPCError
// ===========================================================================

func TestMapGRPCError_NotFound(t *testing.T) {
	grpcErr := status.Error(codes.NotFound, "key /foo not found")
	err := mapGRPCError("Get", grpcErr)
	var knf *transport.KeyNotFoundError
	if !errors.As(err, &knf) {
		t.Fatalf("expected KeyNotFoundError, got %T: %v", err, err)
	}
}

func TestMapGRPCError_OtherCode(t *testing.T) {
	grpcErr := status.Error(codes.Internal, "internal server error")
	err := mapGRPCError("Put", grpcErr)
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected RequestError, got %T: %v", err, err)
	}
	if reqErr.Method != "Put" {
		t.Errorf("expected method Put, got %q", reqErr.Method)
	}
}

func TestMapGRPCError_NonGRPCError(t *testing.T) {
	// A plain error (not a gRPC status) should also become a RequestError.
	plainErr := errors.New("connection reset")
	err := mapGRPCError("Delete", plainErr)
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected RequestError, got %T: %v", err, err)
	}
	if reqErr.Method != "Delete" {
		t.Errorf("expected method Delete, got %q", reqErr.Method)
	}
}

func TestMapGRPCError_Unavailable(t *testing.T) {
	grpcErr := status.Error(codes.Unavailable, "server unavailable")
	err := mapGRPCError("List", grpcErr)
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected RequestError, got %T: %v", err, err)
	}
	if reqErr.StatusCode != int(codes.Unavailable) {
		t.Errorf("expected status %d, got %d", codes.Unavailable, reqErr.StatusCode)
	}
}

// ===========================================================================
// Request method — error from Invoke (disconnected conn)
// ===========================================================================

func TestUnixRequest_InvokeError(t *testing.T) {
	// A Transport with a nil conn will panic on Invoke. Use a real (but
	// unconnected) server address so Connect fails and we can't call Invoke.
	// Instead, verify the closed path.
	tr := &Transport{config: &transport.Config{}}
	tr.closed.Store(true)

	err := tr.Request(context.Background(), "/some.Method", nil, nil)
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}
