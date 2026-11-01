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
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestHandleVectorUpdateNamespace_Success(t *testing.T) {
	var got *service.UpdateNamespaceRequest
	mock := &mockVectorService{
		updateNamespaceFn: func(_ context.Context, req *service.UpdateNamespaceRequest) (*service.UpdateNamespaceResponse, error) {
			got = req
			return &service.UpdateNamespaceResponse{Namespace: req.Namespace, RequiresRebuild: true, Success: true, Timestamp: 3000}, nil
		},
	}
	srv := newVectorServer(mock)
	payload, _ := proto.Marshal(&pb.VectorUpdateNamespaceRequest{
		Namespace:       "ns",
		Dimension:       4,
		IndexParamsJson: `{"ef_search":"128"}`,
		MetadataJson:    `{"owner":"search"}`,
	})

	resp, err := srv.handleVectorUpdateNamespace(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out pb.VectorUpdateNamespaceResponse
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if !out.GetUpdated() || !out.GetRequiresRebuild() || out.GetTimestamp() != 3000 {
		t.Fatalf("response = %+v", &out)
	}
	if got.Namespace != "ns" || got.Dimension != 4 || got.IndexConfig["ef_search"] != "128" || got.Metadata["owner"] != "search" {
		t.Fatalf("service saw %+v", got)
	}
}

func TestHandleVectorUpdateNamespace_NoOptionalFields(t *testing.T) {
	var got *service.UpdateNamespaceRequest
	mock := &mockVectorService{
		updateNamespaceFn: func(_ context.Context, req *service.UpdateNamespaceRequest) (*service.UpdateNamespaceResponse, error) {
			got = req
			return &service.UpdateNamespaceResponse{Success: true}, nil
		},
	}
	srv := newVectorServer(mock)
	payload, _ := proto.Marshal(&pb.VectorUpdateNamespaceRequest{Namespace: "ns"})

	if _, err := srv.handleVectorUpdateNamespace(context.Background(), payload); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.IndexConfig != nil || got.Metadata != nil {
		t.Fatalf("service saw %+v, want no index config or metadata", got)
	}
}

// TestHandleVectorUpdateNamespace_RefusalsReachTheFrame proves the service's
// typed refusals leave the server as a structured refusal a client decodes.
func TestHandleVectorUpdateNamespace_RefusalsReachTheFrame(t *testing.T) {
	cases := map[string]struct {
		err    error
		reason string
	}{
		"missing namespace":  {&service.NamespaceNotFoundError{Namespace: "missing"}, transport.ReasonVectorNamespaceNotFound},
		"dimension mismatch": {&service.DimensionMismatchError{Expected: 4, Actual: 8}, transport.ReasonVectorDimensionMismatch},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			mock := &mockVectorService{
				updateNamespaceFn: func(context.Context, *service.UpdateNamespaceRequest) (*service.UpdateNamespaceResponse, error) {
					return nil, tc.err
				},
			}
			srv := newVectorServer(mock)
			payload, _ := proto.Marshal(&pb.VectorUpdateNamespaceRequest{Namespace: "ns"})

			_, err := srv.handleVectorUpdateNamespace(context.Background(), payload)
			if !errors.Is(err, tc.err) {
				t.Fatalf("err = %v, want %v", err, tc.err)
			}
			p := errorPayloadOf(err)
			if p.Info == nil || p.Info.Reason != tc.reason {
				t.Fatalf("payload info = %+v, want reason %s", p.Info, tc.reason)
			}
			if st := ServiceErrorStatus(err); st == wire.StatusError {
				t.Fatalf("status = %d, want a classified status", st)
			}
		})
	}
}

func TestHandleVectorUpdateNamespace_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := proto.Marshal(&pb.VectorUpdateNamespaceRequest{Namespace: "ns"})

	_, err := srv.handleVectorUpdateNamespace(context.Background(), payload)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

func TestHandleVectorUpdateNamespace_BadPayloads(t *testing.T) {
	srv := newVectorServer(&mockVectorService{})
	cases := map[string][]byte{
		"unmarshal":    {0xff, 0xff, 0xff},
		"index params": mustProto(t, &pb.VectorUpdateNamespaceRequest{Namespace: "ns", IndexParamsJson: "{"}),
		"metadata":     mustProto(t, &pb.VectorUpdateNamespaceRequest{Namespace: "ns", MetadataJson: "{"}),
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := srv.handleVectorUpdateNamespace(context.Background(), payload); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}
