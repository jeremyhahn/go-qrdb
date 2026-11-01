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

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestE2E_UpdateNamespace_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	var got pb.VectorUpdateNamespaceRequest
	srv.SetResponder("vector.UpdateNamespace", func(payload []byte) (uint16, []byte) {
		if err := proto.Unmarshal(payload, &got); err != nil {
			return wire.StatusInvalidArgument, nil
		}
		return wire.StatusOK, mustMarshal(&pb.VectorUpdateNamespaceResponse{
			Updated: true, RequiresRebuild: true, Timestamp: 100,
		})
	})
	resp, err := tr.UpdateNamespace(context.Background(), &transport.UpdateNamespaceRequest{
		Namespace:   "ns",
		Dimension:   4,
		IndexConfig: map[string]string{"ef_search": "128"},
		Metadata:    map[string]interface{}{"owner": "search"},
	})
	if err != nil {
		t.Fatalf("UpdateNamespace: %v", err)
	}
	if !resp.Success || !resp.RequiresRebuild || resp.Namespace != "ns" || resp.Timestamp != 100 {
		t.Fatalf("resp = %+v", resp)
	}
	if got.GetNamespace() != "ns" || got.GetDimension() != 4 ||
		got.GetIndexParamsJson() != `{"ef_search":"128"}` || got.GetMetadataJson() != `{"owner":"search"}` {
		t.Fatalf("request = %+v", &got)
	}
}

// vectorRefusalBody encodes the error frame a server sends for a vector
// refusal. The e2e harness frames every error body as plain message text, so
// the refusal's decoding is exercised on mapWireError directly.
func vectorRefusalBody(t *testing.T, status uint16, reason string, refusal interface {
	error
	Metadata() map[string]string
}) []byte {
	t.Helper()
	body, err := wire.EncodeErrorPayload(status, wire.ErrorPayload{
		Message: refusal.Error(),
		Info:    &wire.ErrorInfo{Domain: transport.RefusalDomain, Reason: reason, Metadata: refusal.Metadata()},
	})
	if err != nil {
		t.Fatalf("encode error payload: %v", err)
	}
	return body
}

func TestMapWireError_vectorNamespaceNotFoundIsTyped(t *testing.T) {
	err := mapWireError("vector.UpdateNamespace", wire.StatusNotFound, vectorRefusalBody(t, wire.StatusNotFound,
		transport.ReasonVectorNamespaceNotFound, &transport.NamespaceNotFoundError{Namespace: "missing"}))
	nf, ok := errors.AsType[*transport.NamespaceNotFoundError](err)
	if !ok || nf.Namespace != "missing" {
		t.Fatalf("err = %v (%T), want *NamespaceNotFoundError for missing", err, err)
	}
	if _, isKey := errors.AsType[*transport.KeyNotFoundError](err); isKey {
		t.Fatalf("err = %v, a missing namespace must not read as a missing key", err)
	}
}

func TestMapWireError_vectorDimensionMismatchIsTyped(t *testing.T) {
	err := mapWireError("vector.UpdateNamespace", wire.StatusInvalidArgument, vectorRefusalBody(t, wire.StatusInvalidArgument,
		transport.ReasonVectorDimensionMismatch, &transport.DimensionMismatchError{Expected: 4, Got: 8}))
	dm, ok := errors.AsType[*transport.DimensionMismatchError](err)
	if !ok || dm.Expected != 4 || dm.Got != 8 {
		t.Fatalf("err = %v (%T), want *DimensionMismatchError 4/8", err, err)
	}
}

func TestE2E_UpdateNamespace_ServerError(t *testing.T) {
	srv, tr := startE2EServer(t)
	serverErr(srv, "vector.UpdateNamespace", "boom")
	if _, err := tr.UpdateNamespace(context.Background(), &transport.UpdateNamespaceRequest{Namespace: "ns"}); err == nil {
		t.Fatal("UpdateNamespace succeeded, want the server's error")
	}
}

func TestE2E_UpdateNamespace_Closed(t *testing.T) {
	_, tr := startE2EServer(t)
	if err := tr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_, err := tr.UpdateNamespace(context.Background(), &transport.UpdateNamespaceRequest{Namespace: "ns"})
	if _, ok := errors.AsType[*transport.ClosedError](err); !ok {
		t.Fatalf("err = %v, want *ClosedError", err)
	}
}
