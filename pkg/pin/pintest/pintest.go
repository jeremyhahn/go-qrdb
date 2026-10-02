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

// Package pintest provides a real pin.SoftwareBackend over in-memory storage
// for tests that need an SO/User PIN store. The backend is the production
// implementation — the PINs are hashed and compared in constant time exactly
// as on a node — with only the Argon2id cost lowered so tests stay fast. It
// is imported by tests only; a production import of it is a defect.
package pintest

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/pin"
)

// Storage is an in-memory pin.Storage.
type Storage struct {
	mu sync.Mutex
	m  map[string][]byte
}

var _ pin.Storage = (*Storage)(nil)

// NewStorage returns an empty in-memory pin.Storage.
func NewStorage() *Storage {
	return &Storage{m: map[string][]byte{}}
}

// Get implements pin.Storage.
func (s *Storage) Get(_ context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[key]
	if !ok {
		return nil, pin.ErrNotFound
	}
	return append([]byte(nil), v...), nil
}

// Put implements pin.Storage.
func (s *Storage) Put(_ context.Context, key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[key] = append([]byte(nil), value...)
	return nil
}

// HashConfig is a low-cost Argon2id configuration for tests.
func HashConfig() pin.HashConfig {
	return pin.HashConfig{Algorithm: pin.HashArgon2id, Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32, SaltLen: 16}
}

// NewBackend returns a software PIN backend over fresh in-memory storage.
func NewBackend(tb testing.TB) *pin.SoftwareBackend {
	tb.Helper()
	b, err := pin.NewSoftwareBackend(NewStorage(), HashConfig())
	if err != nil {
		tb.Fatalf("pintest: new software backend: %v", err)
	}
	return b
}

// ThrottledSOPIN is the SO PIN NewSOPINBackend provisions.
const ThrottledSOPIN = "so-pin-1234"

// NewSOPINBackend is a raw (undecorated) backend with ThrottledSOPIN set: what
// a composer that forgot the lockout would hand a transport.
func NewSOPINBackend(tb testing.TB) *pin.SoftwareBackend {
	tb.Helper()
	b := NewBackend(tb)
	if err := b.SetSOPIN("", ThrottledSOPIN); err != nil {
		tb.Fatalf("pintest: set SO PIN: %v", err)
	}
	return b
}

// RequireThrottled fails the test unless SO PIN checks through backend -- the
// backend a transport holds after being handed NewSOPINBackend's raw backend
// -- are held to the SO PIN lockout: after the free wrong PINs, even
// ThrottledSOPIN is refused with pin.ErrPINLocked.
func RequireThrottled(tb testing.TB, backend pin.PINBackend) {
	tb.Helper()
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if err := pin.RequireSOPIN(ctx, backend, "wrong-pin-0000"); !errors.Is(err, pin.ErrPINInvalid) {
			tb.Fatalf("pintest: wrong PIN %d = %v, want pin.ErrPINInvalid", i+1, err)
		}
	}
	if err := pin.RequireSOPIN(ctx, backend, ThrottledSOPIN); !errors.Is(err, pin.ErrPINLocked) {
		tb.Fatalf("pintest: the right PIN after the free failures = %v, want pin.ErrPINLocked: the backend is not throttled", err)
	}
}
