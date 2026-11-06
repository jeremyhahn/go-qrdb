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
)

// TestGRPCScan_Success verifies Scan returns every key under the prefix with
// its value. Exists and the KV reads are checked across every transport by
// sdk/go/conformance; Scan's result is checked only here.
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
	if len(result) != 2 || string(result["a"]) != "1" || string(result["b"]) != "2" {
		t.Fatalf("Scan = %q, want a=1 b=2", result)
	}
}

// TestGRPCCapabilities_Success verifies the server's KV capability flag
// reaches the caller.
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
