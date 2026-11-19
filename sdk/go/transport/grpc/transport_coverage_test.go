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

package grpc

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ===========================================================================
// Scan / Exists / Capabilities success tests
// ===========================================================================

func TestGRPCScan_Success(t *testing.T) {
	kvSvc := newMockKVService()
	_ = kvSvc.Put(context.Background(), "a", []byte("1"))
	_ = kvSvc.Put(context.Background(), "b", []byte("2"))
	tr, cleanup := connectTransport(t, kvSvc, newMockAdminService())
	defer cleanup()

	result, err := tr.Scan(context.Background(), "")
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("got %d entries", len(result))
	}
}

func TestGRPCCapabilities_Success(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	caps, err := tr.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if !caps.KV {
		t.Fatal("expected KV capability")
	}
}

// ===========================================================================
// applyReadMetadata / applyWriteMetadata coverage
// ===========================================================================

func TestGRPCMetadata_AllOptions(t *testing.T) {
	kvSvc := newMockKVService()
	_ = kvSvc.Put(context.Background(), "k", []byte("v"))
	tr, cleanup := connectTransport(t, kvSvc, newMockAdminService())
	defer cleanup()

	// Exercise all branches of applyReadMetadata.
	if _, err := tr.Get(context.Background(), "k",
		transport.WithConsistency(transport.ConsistencyLinearizable),
		transport.WithReadTimeout(100),
		transport.WithAllowStale(),
	); err != nil {
		t.Fatalf("Get: %v", err)
	}

	// Exercise write metadata.
	if err := tr.Put(context.Background(), "k", []byte("v"),
		transport.WithWriteMode(transport.WriteModeBatched)); err != nil {
		t.Fatalf("Put: %v", err)
	}
}
