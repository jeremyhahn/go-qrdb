// Copyright (c) 2026 Jeremy Hahn
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

package quic

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/pin/pintest"
)

// TestWithPINBackend_RawBackendIsThrottled: a raw backend handed straight to
// this transport's WithPINBackend is decorated with the SO PIN lockout, so
// the SO PIN checks its handlers make are still throttled.
func TestWithPINBackend_RawBackendIsThrottled(t *testing.T) {
	srv := NewServer(nil, nil, "127.0.0.1:0", WithPINBackend(pintest.NewSOPINBackend(t)))
	pintest.RequireThrottled(t, srv.pinBackend)
}

// mockPINBackend is a pin.PINBackend whose every operation succeeds.
type mockPINBackend struct{}

var _ pin.PINBackend = (*mockPINBackend)(nil)

func (m *mockPINBackend) Strategy() pin.StrategyID             { return pin.StrategySoftware }
func (m *mockPINBackend) SetSOPIN(_, _ string) error           { return nil }
func (m *mockPINBackend) SetUserPIN(_, _ string) error         { return nil }
func (m *mockPINBackend) ChangeSOPIN(_, _ string) error        { return nil }
func (m *mockPINBackend) ChangeUserPIN(_, _ string) error      { return nil }
func (m *mockPINBackend) VerifySOPIN(_ string) error           { return nil }
func (m *mockPINBackend) VerifyUserPIN(_ string) error         { return nil }
func (m *mockPINBackend) SOPINSet() bool                       { return false }
func (m *mockPINBackend) UserPINSet() bool                     { return false }
func (m *mockPINBackend) ClearPINs() error                     { return nil }
func (m *mockPINBackend) IsInitialized() bool                  { return true }
func (m *mockPINBackend) GetLockoutStatus() *pin.LockoutStatus { return &pin.LockoutStatus{} }
func (m *mockPINBackend) ResetLockout(_ string) error          { return nil }
