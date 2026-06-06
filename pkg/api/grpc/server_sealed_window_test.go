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

import "testing"

// TestWithSealedWindowProbe_StoresProbe verifies the option threads
// through NewServer and lands on the Server struct so Start() will
// install the interceptor when the gRPC server boots.
func TestWithSealedWindowProbe_StoresProbe(t *testing.T) {
	probe := newProbe(true)
	srv := NewServer(nil, nil, "127.0.0.1:0", WithSealedWindowProbe(probe))
	if srv.sealedWindowProbe == nil {
		t.Fatalf("WithSealedWindowProbe did not set sealedWindowProbe on Server")
	}
	if got := srv.sealedWindowProbe.IsSealed(); !got {
		t.Errorf("probe IsSealed() = %v, want true", got)
	}
}

// TestNoSealedWindowProbe_NotStored verifies that omitting the option
// leaves sealedWindowProbe nil, in which case Start() must not install
// the interceptor.
func TestNoSealedWindowProbe_NotStored(t *testing.T) {
	srv := NewServer(nil, nil, "127.0.0.1:0")
	if srv.sealedWindowProbe != nil {
		t.Fatalf("sealedWindowProbe = %v, want nil", srv.sealedWindowProbe)
	}
}
