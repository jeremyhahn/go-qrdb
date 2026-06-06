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
	"testing"
)

// TestE2E_Harness_Smoke ensures the in-process QUIC harness wiring is sound:
// startE2EServer can stand up a real QUIC server, the SDK Transport can
// connect, run a health check, and shut down cleanly.
func TestE2E_Harness_Smoke(t *testing.T) {
	srv, tr := startE2EServer(t)
	if srv == nil || tr == nil {
		t.Fatal("startE2EServer returned nil")
	}

	resp, err := tr.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if resp.Status != "ok" {
		t.Fatalf("Status = %q, want ok", resp.Status)
	}
}
