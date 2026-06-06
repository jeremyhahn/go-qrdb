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

package pin

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
)

// stubBackend is a controllable PINBackend used to drive Service edges.
type stubBackend struct {
	strategy        StrategyID
	setSOPINErr     error
	setUserPINErr   error
	changeSOPINErr  error
	changeUserErr   error
	verifySOErr     error
	verifyUserErr   error
	initialized     bool
	soSet           bool
	userSet         bool
	lockoutStatus   *LockoutStatus
	resetLockoutErr error

	setSOPINCalls   int32
	setUserPINCalls int32
	changeSOCalls   int32
	changeUserCalls int32
	verifySOCalls   int32
	verifyUserCalls int32
	resetCalls      int32
}

func (s *stubBackend) Strategy() StrategyID { return s.strategy }

func (s *stubBackend) SetSOPIN(_, _ string) error {
	atomic.AddInt32(&s.setSOPINCalls, 1)
	return s.setSOPINErr
}

func (s *stubBackend) SetUserPIN(_, _ string) error {
	atomic.AddInt32(&s.setUserPINCalls, 1)
	return s.setUserPINErr
}

func (s *stubBackend) ChangeSOPIN(_, _ string) error {
	atomic.AddInt32(&s.changeSOCalls, 1)
	return s.changeSOPINErr
}

func (s *stubBackend) ChangeUserPIN(_, _ string) error {
	atomic.AddInt32(&s.changeUserCalls, 1)
	return s.changeUserErr
}

func (s *stubBackend) VerifySOPIN(_ string) error {
	atomic.AddInt32(&s.verifySOCalls, 1)
	return s.verifySOErr
}

func (s *stubBackend) VerifyUserPIN(_ string) error {
	atomic.AddInt32(&s.verifyUserCalls, 1)
	return s.verifyUserErr
}

func (s *stubBackend) IsInitialized() bool              { return s.initialized }
func (s *stubBackend) SOPINSet() bool                   { return s.soSet }
func (s *stubBackend) UserPINSet() bool                 { return s.userSet }
func (s *stubBackend) GetLockoutStatus() *LockoutStatus { return s.lockoutStatus }
func (s *stubBackend) ResetLockout(_ string) error {
	atomic.AddInt32(&s.resetCalls, 1)
	return s.resetLockoutErr
}

// stubVerifierBackend layers FIDO2HashVerifier onto stubBackend.
type stubVerifierBackend struct {
	stubBackend
	fido2Result bool
	fido2Hash   []byte
}

func (s *stubVerifierBackend) VerifyFIDO2Hash(hash []byte) bool {
	s.fido2Hash = append(s.fido2Hash[:0], hash...)
	return s.fido2Result
}

// silentLogger discards output during tests.
func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// =============================================================================
// Constructor
// =============================================================================

func TestNewService_NilLoggerDefaults(t *testing.T) {
	svc := NewService(&stubBackend{strategy: StrategySoftware}, nil)
	if svc == nil {
		t.Fatal("NewService returned nil")
	}
	if svc.log == nil {
		t.Fatal("nil logger should default to slog.Default()")
	}
}

func TestNewService_ProvidedLoggerKept(t *testing.T) {
	log := silentLogger()
	svc := NewService(&stubBackend{strategy: StrategySoftware}, log)
	if svc.log != log {
		t.Fatal("provided logger was replaced")
	}
}

// =============================================================================
// Backend / Strategy accessors
// =============================================================================

func TestService_BackendAndStrategy(t *testing.T) {
	backend := &stubBackend{strategy: StrategyTPM2}
	svc := NewService(backend, silentLogger())
	if svc.Backend() != backend {
		t.Fatal("Backend mismatch")
	}
	if svc.Strategy() != StrategyTPM2 {
		t.Fatalf("Strategy = %q, want %q", svc.Strategy(), StrategyTPM2)
	}
}

// =============================================================================
// SetSOPIN / SetUserPIN / ChangeSOPIN / ChangeUserPIN
// =============================================================================

func TestService_SetSOPIN_TooShort(t *testing.T) {
	b := &stubBackend{strategy: StrategySoftware}
	svc := NewService(b, silentLogger())
	if err := svc.SetSOPIN("", "abc"); !errors.Is(err, ErrPINTooShort) {
		t.Fatalf("err = %v, want ErrPINTooShort", err)
	}
	if got := atomic.LoadInt32(&b.setSOPINCalls); got != 0 {
		t.Fatalf("backend SetSOPIN called %d times, want 0 (validation must short-circuit)", got)
	}
}

func TestService_SetSOPIN_DelegatesToBackend(t *testing.T) {
	b := &stubBackend{strategy: StrategySoftware}
	svc := NewService(b, silentLogger())
	if err := svc.SetSOPIN("", "long-enough"); err != nil {
		t.Fatalf("SetSOPIN: %v", err)
	}
	if got := atomic.LoadInt32(&b.setSOPINCalls); got != 1 {
		t.Fatalf("setSOPINCalls = %d, want 1", got)
	}
}

func TestService_SetSOPIN_BackendErrorPropagates(t *testing.T) {
	want := errors.New("backend failed")
	b := &stubBackend{strategy: StrategySoftware, setSOPINErr: want}
	svc := NewService(b, silentLogger())
	if err := svc.SetSOPIN("", "long-enough"); !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

func TestService_SetUserPIN_TooShort(t *testing.T) {
	b := &stubBackend{strategy: StrategySoftware}
	svc := NewService(b, silentLogger())
	if err := svc.SetUserPIN("so", "abc"); !errors.Is(err, ErrPINTooShort) {
		t.Fatalf("err = %v, want ErrPINTooShort", err)
	}
}

func TestService_SetUserPIN_HappyPath_PushesFIDO2Hash(t *testing.T) {
	b := &stubBackend{strategy: StrategySoftware}
	svc := NewService(b, silentLogger())

	var captured []byte
	var mu sync.Mutex
	svc.SetFIDO2HashSetter(func(hash []byte) {
		mu.Lock()
		defer mu.Unlock()
		captured = append([]byte(nil), hash...)
	})

	if err := svc.SetUserPIN("so", "user-pin"); err != nil {
		t.Fatalf("SetUserPIN: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	want := ComputeFIDO2PINHash("user-pin")
	if !bytes.Equal(captured, want) {
		t.Fatalf("captured = %x, want %x", captured, want)
	}
}

func TestService_SetUserPIN_BackendErrorSkipsPush(t *testing.T) {
	b := &stubBackend{strategy: StrategySoftware, setUserPINErr: errors.New("nope")}
	svc := NewService(b, silentLogger())

	pushed := false
	svc.SetFIDO2HashSetter(func(_ []byte) { pushed = true })

	if err := svc.SetUserPIN("so", "user-pin"); err == nil {
		t.Fatal("SetUserPIN: want err, got nil")
	}
	if pushed {
		t.Fatal("FIDO2 hash must not be pushed on backend error")
	}
}

func TestService_ChangeSOPIN_TooShort(t *testing.T) {
	b := &stubBackend{strategy: StrategySoftware}
	svc := NewService(b, silentLogger())
	if err := svc.ChangeSOPIN("so", "ab"); !errors.Is(err, ErrPINTooShort) {
		t.Fatalf("err = %v, want ErrPINTooShort", err)
	}
}

func TestService_ChangeSOPIN_Delegates(t *testing.T) {
	b := &stubBackend{strategy: StrategySoftware}
	svc := NewService(b, silentLogger())
	if err := svc.ChangeSOPIN("old-so", "new-so-long"); err != nil {
		t.Fatalf("ChangeSOPIN: %v", err)
	}
	if got := atomic.LoadInt32(&b.changeSOCalls); got != 1 {
		t.Fatalf("changeSOCalls = %d, want 1", got)
	}
}

func TestService_ChangeUserPIN_TooShort(t *testing.T) {
	b := &stubBackend{strategy: StrategySoftware}
	svc := NewService(b, silentLogger())
	if err := svc.ChangeUserPIN("cur", "ab"); !errors.Is(err, ErrPINTooShort) {
		t.Fatalf("err = %v, want ErrPINTooShort", err)
	}
}

func TestService_ChangeUserPIN_HappyPath_PushesFIDO2Hash(t *testing.T) {
	b := &stubBackend{strategy: StrategySoftware}
	svc := NewService(b, silentLogger())

	var captured []byte
	svc.SetFIDO2HashSetter(func(hash []byte) {
		captured = append([]byte(nil), hash...)
	})

	if err := svc.ChangeUserPIN("cur-pin", "new-pin"); err != nil {
		t.Fatalf("ChangeUserPIN: %v", err)
	}
	want := ComputeFIDO2PINHash("new-pin")
	if !bytes.Equal(captured, want) {
		t.Fatalf("captured = %x, want %x", captured, want)
	}
}

func TestService_ChangeUserPIN_BackendErrorSkipsPush(t *testing.T) {
	b := &stubBackend{strategy: StrategySoftware, changeUserErr: errors.New("no")}
	svc := NewService(b, silentLogger())

	pushed := false
	svc.SetFIDO2HashSetter(func(_ []byte) { pushed = true })

	if err := svc.ChangeUserPIN("cur", "new-pin"); err == nil {
		t.Fatal("ChangeUserPIN: want err")
	}
	if pushed {
		t.Fatal("FIDO2 hash must not be pushed on backend error")
	}
}

// =============================================================================
// Verify
// =============================================================================

func TestService_VerifySOPIN_Delegates(t *testing.T) {
	b := &stubBackend{strategy: StrategySoftware}
	svc := NewService(b, silentLogger())
	if err := svc.VerifySOPIN("so"); err != nil {
		t.Fatalf("VerifySOPIN: %v", err)
	}
	if got := atomic.LoadInt32(&b.verifySOCalls); got != 1 {
		t.Fatalf("verifySOCalls = %d, want 1", got)
	}
}

func TestService_VerifyUserPIN_HappyPath_PushesFIDO2Hash(t *testing.T) {
	b := &stubBackend{strategy: StrategySoftware}
	svc := NewService(b, silentLogger())

	var captured []byte
	svc.SetFIDO2HashSetter(func(hash []byte) {
		captured = append([]byte(nil), hash...)
	})

	if err := svc.VerifyUserPIN("user-pin"); err != nil {
		t.Fatalf("VerifyUserPIN: %v", err)
	}
	want := ComputeFIDO2PINHash("user-pin")
	if !bytes.Equal(captured, want) {
		t.Fatalf("captured = %x, want %x", captured, want)
	}
}

func TestService_VerifyUserPIN_BackendErrorSkipsPush(t *testing.T) {
	b := &stubBackend{strategy: StrategySoftware, verifyUserErr: errors.New("bad")}
	svc := NewService(b, silentLogger())

	pushed := false
	svc.SetFIDO2HashSetter(func(_ []byte) { pushed = true })

	if err := svc.VerifyUserPIN("user-pin"); err == nil {
		t.Fatal("VerifyUserPIN: want err")
	}
	if pushed {
		t.Fatal("FIDO2 hash must not be pushed on backend error")
	}
}

// =============================================================================
// State accessors
// =============================================================================

func TestService_StateAccessors(t *testing.T) {
	status := &LockoutStatus{FailedAttempts: 2, MaxAttempts: 5}
	b := &stubBackend{
		strategy:      StrategySoftware,
		initialized:   true,
		soSet:         true,
		userSet:       true,
		lockoutStatus: status,
	}
	svc := NewService(b, silentLogger())

	if !svc.IsInitialized() {
		t.Fatal("IsInitialized: want true")
	}
	if !svc.IsPINSet() {
		t.Fatal("IsPINSet: want true")
	}
	if !svc.SOPINSet() {
		t.Fatal("SOPINSet: want true")
	}
	if !svc.UserPINSet() {
		t.Fatal("UserPINSet: want true")
	}
	if svc.GetLockoutStatus() != status {
		t.Fatal("GetLockoutStatus must surface backend status")
	}
}

func TestService_ResetLockout_Delegates(t *testing.T) {
	b := &stubBackend{strategy: StrategySoftware}
	svc := NewService(b, silentLogger())
	if err := svc.ResetLockout("so"); err != nil {
		t.Fatalf("ResetLockout: %v", err)
	}
	if got := atomic.LoadInt32(&b.resetCalls); got != 1 {
		t.Fatalf("resetCalls = %d, want 1", got)
	}
}

func TestService_ResetLockout_PropagatesError(t *testing.T) {
	want := errors.New("reset fail")
	b := &stubBackend{strategy: StrategySoftware, resetLockoutErr: want}
	svc := NewService(b, silentLogger())
	if err := svc.ResetLockout("so"); !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

// =============================================================================
// VerifyFIDO2Hash
// =============================================================================

func TestService_VerifyFIDO2Hash_WrongLength(t *testing.T) {
	svc := NewService(&stubBackend{strategy: StrategySoftware}, silentLogger())
	if svc.VerifyFIDO2Hash(nil) {
		t.Fatal("nil hash must return false")
	}
	if svc.VerifyFIDO2Hash([]byte{1, 2, 3}) {
		t.Fatal("short hash must return false")
	}
}

func TestService_VerifyFIDO2Hash_BackendDoesNotImplementVerifier(t *testing.T) {
	svc := NewService(&stubBackend{strategy: StrategySoftware}, silentLogger())
	if svc.VerifyFIDO2Hash(make([]byte, FIDO2PINHashSize)) {
		t.Fatal("backend without verifier must return false")
	}
}

func TestService_VerifyFIDO2Hash_DelegatesToBackend(t *testing.T) {
	verifier := &stubVerifierBackend{
		stubBackend: stubBackend{strategy: StrategySoftware},
		fido2Result: true,
	}
	svc := NewService(verifier, silentLogger())
	hash := make([]byte, FIDO2PINHashSize)
	for i := range hash {
		hash[i] = byte(i)
	}
	if !svc.VerifyFIDO2Hash(hash) {
		t.Fatal("VerifyFIDO2Hash: want true")
	}
	if !bytes.Equal(verifier.fido2Hash, hash) {
		t.Fatalf("backend received %x, want %x", verifier.fido2Hash, hash)
	}
}

// =============================================================================
// Top-level hash helpers
// =============================================================================

func TestComputeFIDO2PINHash_Deterministic(t *testing.T) {
	a := ComputeFIDO2PINHash("user-pin")
	b := ComputeFIDO2PINHash("user-pin")
	if !bytes.Equal(a, b) {
		t.Fatal("ComputeFIDO2PINHash must be deterministic")
	}
	if len(a) != FIDO2PINHashSize {
		t.Fatalf("len = %d, want %d", len(a), FIDO2PINHashSize)
	}
}

func TestComputeFIDO2PINHash_DifferentInputs(t *testing.T) {
	a := ComputeFIDO2PINHash("pin-one")
	b := ComputeFIDO2PINHash("pin-two")
	if bytes.Equal(a, b) {
		t.Fatal("distinct PINs must produce distinct hashes")
	}
}

func TestVerifyFIDO2PINHash_LengthMismatch(t *testing.T) {
	if VerifyFIDO2PINHash(nil, make([]byte, FIDO2PINHashSize)) {
		t.Fatal("nil expected must return false")
	}
	if VerifyFIDO2PINHash(make([]byte, FIDO2PINHashSize), nil) {
		t.Fatal("nil actual must return false")
	}
}

func TestVerifyFIDO2PINHash_Match(t *testing.T) {
	a := ComputeFIDO2PINHash("pin")
	b := ComputeFIDO2PINHash("pin")
	if !VerifyFIDO2PINHash(a, b) {
		t.Fatal("equal hashes must verify")
	}
}

func TestVerifyFIDO2PINHash_Mismatch(t *testing.T) {
	a := ComputeFIDO2PINHash("pin-one")
	b := ComputeFIDO2PINHash("pin-two")
	if VerifyFIDO2PINHash(a, b) {
		t.Fatal("distinct hashes must not verify")
	}
}

// =============================================================================
// pushFIDO2Hash internals
// =============================================================================

func TestService_PushFIDO2Hash_NilSetterIsNoop(t *testing.T) {
	svc := NewService(&stubBackend{strategy: StrategySoftware}, silentLogger())
	// No setter registered — must not panic.
	svc.pushFIDO2Hash("user-pin")
}
