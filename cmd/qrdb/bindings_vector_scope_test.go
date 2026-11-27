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

//go:build qrdb_vector && !integration

package main

import (
	"context"
	"testing"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// scopeRecordingTransport is a vector transport that knows which database
// it is scoped to and records every vector call as "<database>:<op>".
// Only the operations the desktop vector editor, batch upsert and hybrid
// search use are implemented; any other call panics on the nil embed.
type scopeRecordingTransport struct {
	transport.VectorClient
	database string
	calls    *[]string
}

func (s *scopeRecordingTransport) Connect(context.Context) error { return nil }
func (s *scopeRecordingTransport) Close() error                  { return nil }
func (s *scopeRecordingTransport) Healthy(context.Context) bool  { return true }
func (s *scopeRecordingTransport) Request(context.Context, string, any, any) error {
	return nil
}

func (s *scopeRecordingTransport) WithDatabase(database string) (transport.Transport, error) {
	return &scopeRecordingTransport{database: transport.NormalizeDatabaseScope(database), calls: s.calls}, nil
}

func (s *scopeRecordingTransport) record(op string) {
	*s.calls = append(*s.calls, s.database+":"+op)
}

func (s *scopeRecordingTransport) GetVector(context.Context, *transport.GetVectorRequest) (*transport.GetVectorResponse, error) {
	s.record("GetVector")
	return &transport.GetVectorResponse{}, nil
}

func (s *scopeRecordingTransport) UpdateMetadata(context.Context, *transport.UpdateVectorMetadataRequest) (*transport.UpdateVectorMetadataResponse, error) {
	s.record("UpdateMetadata")
	return &transport.UpdateVectorMetadataResponse{}, nil
}

func (s *scopeRecordingTransport) BatchUpsert(context.Context, *transport.BatchUpsertRequest) (*transport.BatchUpsertResponse, error) {
	s.record("BatchUpsert")
	return &transport.BatchUpsertResponse{}, nil
}

func (s *scopeRecordingTransport) HybridSearch(context.Context, *transport.HybridSearchRequest) (*transport.HybridSearchResponse, error) {
	s.record("HybridSearch")
	return &transport.HybridSearchResponse{}, nil
}

// TestVecBinding_OperationsTargetTheSelectedDatabase proves that the
// database the frontend passes reaches the transport: operations for
// "alpha" run in alpha, those for "beta" in beta, and an empty name or
// "system" stays on the unscoped connection.
func TestVecBinding_OperationsTargetTheSelectedDatabase(t *testing.T) {
	var calls []string
	cl, err := sdk.NewClient(&scopeRecordingTransport{calls: &calls})
	if err != nil {
		t.Fatalf("sdk.NewClient: %v", err)
	}
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	conn.SetClient(cl)
	b := NewVectorBinding(log, conn)

	if _, err := b.GetVector("alpha", &transport.GetVectorRequest{Namespace: "ns", ID: 1}); err != nil {
		t.Fatalf("GetVector(alpha): %v", err)
	}
	if _, err := b.UpdateMetadata("alpha", &transport.UpdateVectorMetadataRequest{Namespace: "ns", ID: 1}); err != nil {
		t.Fatalf("UpdateMetadata(alpha): %v", err)
	}
	if _, err := b.BatchUpsert("beta", &transport.BatchUpsertRequest{Namespace: "ns"}); err != nil {
		t.Fatalf("BatchUpsert(beta): %v", err)
	}
	if _, err := b.HybridSearch("beta", &transport.HybridSearchRequest{Namespace: "ns"}); err != nil {
		t.Fatalf("HybridSearch(beta): %v", err)
	}
	if _, err := b.UpdateMetadata("", &transport.UpdateVectorMetadataRequest{Namespace: "ns", ID: 1}); err != nil {
		t.Fatalf("UpdateMetadata(unscoped): %v", err)
	}
	if _, err := b.UpdateMetadata("system", &transport.UpdateVectorMetadataRequest{Namespace: "ns", ID: 1}); err != nil {
		t.Fatalf("UpdateMetadata(system): %v", err)
	}

	want := []string{
		"alpha:GetVector",
		"alpha:UpdateMetadata",
		"beta:BatchUpsert",
		"beta:HybridSearch",
		":UpdateMetadata",
		":UpdateMetadata",
	}
	if len(calls) != len(want) {
		t.Fatalf("calls = %q, want %q", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("calls = %q, want %q", calls, want)
		}
	}
}
