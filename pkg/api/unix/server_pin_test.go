//go:build codec_json && raft

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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/pin"
)

// stubPINBackend is a minimal pin.PINBackend used by the unix server PIN
// tests. The unix server is a thin shell over the gRPC RegisterService
// path; behavioural PIN coverage lives in pkg/api/grpc tests. This file
// asserts the WithPINBackend plumbing is wired correctly.
type stubPINBackend struct{}

func (b *stubPINBackend) Strategy() pin.StrategyID             { return pin.StrategySoftware }
func (b *stubPINBackend) SetSOPIN(_, _ string) error           { return nil }
func (b *stubPINBackend) SetUserPIN(_, _ string) error         { return nil }
func (b *stubPINBackend) ChangeSOPIN(_, _ string) error        { return nil }
func (b *stubPINBackend) ChangeUserPIN(_, _ string) error      { return nil }
func (b *stubPINBackend) VerifySOPIN(_ string) error           { return nil }
func (b *stubPINBackend) VerifyUserPIN(_ string) error         { return nil }
func (b *stubPINBackend) IsInitialized() bool                  { return false }
func (b *stubPINBackend) SOPINSet() bool                       { return false }
func (b *stubPINBackend) UserPINSet() bool                     { return false }
func (b *stubPINBackend) GetLockoutStatus() *pin.LockoutStatus { return nil }
func (b *stubPINBackend) ResetLockout(_ string) error          { return nil }

var _ pin.PINBackend = (*stubPINBackend)(nil)

// TestWithPINBackend_Applied verifies the WithPINBackend option populates
// the pinBackend field on the unix Server struct with the injected backend
// behind the SO PIN lockout, and keeps a backend that is already decorated
// as it is.
func TestWithPINBackend_Applied(t *testing.T) {
	backend := &stubPINBackend{}
	srv := NewServer(newMockKVService(), &mockAdminService{}, "/tmp/test-pinbackend.sock",
		WithPINBackend(backend),
		WithTLS(selfSignedTLSConfig(t)))
	if srv.pinBackend == nil || srv.pinBackend == pin.PINBackend(backend) {
		t.Fatalf("pinBackend = %v, want the injected backend behind the lockout", srv.pinBackend)
	}
	decorated := pin.NewLockoutBackend(backend)
	srv = NewServer(newMockKVService(), &mockAdminService{}, "/tmp/test-pinbackend.sock",
		WithPINBackend(decorated),
		WithTLS(selfSignedTLSConfig(t)))
	if srv.pinBackend != decorated {
		t.Fatalf("pinBackend = %v, want the already-decorated backend kept as it is", srv.pinBackend)
	}
}

// TestWithPINBackend_Nil verifies that supplying a nil pin.PINBackend is
// accepted without panic — preserves dev-mode flows.
func TestWithPINBackend_Nil(t *testing.T) {
	srv := NewServer(newMockKVService(), &mockAdminService{}, "/tmp/test-pinbackend-nil.sock",
		WithPINBackend(nil),
		WithTLS(selfSignedTLSConfig(t)))
	if srv.pinBackend != nil {
		t.Fatal("expected nil pinBackend")
	}
}
