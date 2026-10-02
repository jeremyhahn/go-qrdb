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
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

// memPINStorage is an in-memory Storage for the known-good tests.
type memPINStorage struct {
	m      map[string][]byte
	getErr error
}

func (s *memPINStorage) Get(_ context.Context, key string) ([]byte, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	v, ok := s.m[key]
	if !ok {
		return nil, ErrNotFound
	}
	return v, nil
}

func (s *memPINStorage) Put(_ context.Context, key string, value []byte) error {
	if s.m == nil {
		s.m = map[string][]byte{}
	}
	s.m[key] = value
	return nil
}

// TestSoftwareBackend_KnownGoodRoundTrips persists and reloads the set, reads
// none as empty, refuses a corrupt record, passes a store failure through, and
// persists nothing without storage.
func TestSoftwareBackend_KnownGoodRoundTrips(t *testing.T) {
	store := &memPINStorage{}
	b := &SoftwareBackend{store: store}
	if got, err := b.LoadKnownGood(context.Background(), "node"); err != nil || got != nil {
		t.Fatalf("empty = %v, %v", got, err)
	}
	if err := b.SaveKnownGood(context.Background(), "node", []string{"id:system/officer"}); err != nil {
		t.Fatal(err)
	}
	if got, err := b.LoadKnownGood(context.Background(), "node"); err != nil || len(got) != 1 || got[0] != "id:system/officer" {
		t.Fatalf("reloaded = %v, %v", got, err)
	}
	store.m[soKnownGoodStorageKeyPrefix+"node"] = []byte("{not json")
	var codec *KnownGoodCodecError
	if _, err := b.LoadKnownGood(context.Background(), "node"); !errors.As(err, &codec) || codec.Unwrap() == nil || codec.Error() == "" {
		t.Fatalf("corrupt = %v, want *KnownGoodCodecError", err)
	}
	fault := errors.New("disk")
	store.getErr = fault
	if _, err := b.LoadKnownGood(context.Background(), "node"); !errors.Is(err, fault) {
		t.Fatalf("store failure = %v", err)
	}
	none := &SoftwareBackend{}
	if err := none.SaveKnownGood(context.Background(), "node", []string{"x"}); err != nil {
		t.Fatal(err)
	}
	if got, err := none.LoadKnownGood(context.Background(), "node"); err != nil || got != nil {
		t.Fatalf("no storage = %v, %v", got, err)
	}
}

// TestNewLockoutBackend_OneDecorationPerRawBackend: decorating the same raw
// backend twice yields the same decoration; another backend gets its own.
func TestNewLockoutBackend_OneDecorationPerRawBackend(t *testing.T) {
	raw, other := &SoftwareBackend{}, &SoftwareBackend{}
	if NewLockoutBackend(raw) != NewLockoutBackend(raw) {
		t.Fatal("two decorations of one raw backend")
	}
	if NewLockoutBackend(raw) == NewLockoutBackend(other) {
		t.Fatal("two raw backends share a decoration")
	}
}

// TestLockoutBackend_NodeKnownGoodRecordNamesNoPrincipal: the node SO PIN's
// persisted known-good record holds digests only -- the SO that verified its
// PIN through the decoration is not named in the backend's storage.
func TestLockoutBackend_NodeKnownGoodRecordNamesNoPrincipal(t *testing.T) {
	storage := &memPINStorage{}
	inner, err := NewSoftwareBackend(storage, fastHashConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := inner.SetSOPIN("", lockoutSOPIN); err != nil {
		t.Fatal(err)
	}
	so := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "officer-jane", TenantID: "system"})
	if err := ForCaller(so, newLockoutBackend(inner, nil)).VerifySOPIN(lockoutSOPIN); err != nil {
		t.Fatal(err)
	}
	raw := storage.m[soKnownGoodStorageKeyPrefix+nodeSOPINKey]
	var digests []string
	if err := json.Unmarshal(raw, &digests); err != nil || len(digests) != 1 || len(digests[0]) != 64 {
		t.Fatalf("record = %q (%v), want one 64-hex digest", raw, err)
	}
	if strings.Contains(string(raw), "officer-jane") || strings.Contains(string(raw), "id:") {
		t.Fatalf("record %q names the principal", raw)
	}
}

// valueBackend is a PINBackend whose dynamic type is a struct value, not a
// pointer; extra lets a test make that value unhashable.
type valueBackend struct {
	*SoftwareBackend
	extra any
}

// TestNewLockoutBackend_RefusesANonPointerBackend: a value-typed backend has
// no identity the registry can recognise again, so it is refused -- every SO
// PIN check fails closed with *LockoutBackendKindError and compares nothing,
// the right PIN included -- rather than given a fresh budget per decoration.
// A comparable value that holds an unhashable field is refused the same way
// instead of panicking in the registry.
func TestNewLockoutBackend_RefusesANonPointerBackend(t *testing.T) {
	inner := newProvisionBackend(t)
	if err := inner.SetSOPIN("", lockoutSOPIN); err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string]PINBackend{
		"comparable value": valueBackend{SoftwareBackend: inner},
		"unhashable field": valueBackend{SoftwareBackend: inner, extra: []byte("x")},
	} {
		t.Run(name, func(t *testing.T) {
			var decorated PINBackend
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("NewLockoutBackend panicked: %v", r)
					}
				}()
				decorated = NewLockoutBackend(raw)
			}()
			var kind *LockoutBackendKindError
			for _, b := range []PINBackend{decorated, NewLockoutBackend(raw)} {
				if err := b.VerifySOPIN(lockoutSOPIN); !errors.As(err, &kind) || kind.Type != "pin.valueBackend" || kind.Error() == "" {
					t.Fatalf("the right SO PIN through a value-typed backend = %v, want *LockoutBackendKindError", err)
				}
				if err := ForCaller(context.Background(), b).ChangeSOPIN(lockoutSOPIN, "so-pin-5678"); !errors.As(err, &kind) {
					t.Fatalf("ChangeSOPIN through a value-typed backend = %v, want *LockoutBackendKindError", err)
				}
			}
			if err := inner.VerifySOPIN(lockoutSOPIN); err != nil {
				t.Fatalf("the SO PIN changed through a refused decoration: %v", err)
			}
		})
	}
}
