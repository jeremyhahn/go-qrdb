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
	"errors"
	"testing"
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
	if got, err := b.LoadKnownGood("node"); err != nil || got != nil {
		t.Fatalf("empty = %v, %v", got, err)
	}
	if err := b.SaveKnownGood("node", []string{"id:system/officer"}); err != nil {
		t.Fatal(err)
	}
	if got, err := b.LoadKnownGood("node"); err != nil || len(got) != 1 || got[0] != "id:system/officer" {
		t.Fatalf("reloaded = %v, %v", got, err)
	}
	store.m[soKnownGoodStorageKeyPrefix+"node"] = []byte("{not json")
	var codec *KnownGoodCodecError
	if _, err := b.LoadKnownGood("node"); !errors.As(err, &codec) || codec.Unwrap() == nil || codec.Error() == "" {
		t.Fatalf("corrupt = %v, want *KnownGoodCodecError", err)
	}
	fault := errors.New("disk")
	store.getErr = fault
	if _, err := b.LoadKnownGood("node"); !errors.Is(err, fault) {
		t.Fatalf("store failure = %v", err)
	}
	none := &SoftwareBackend{}
	if err := none.SaveKnownGood("node", []string{"x"}); err != nil {
		t.Fatal(err)
	}
	if got, err := none.LoadKnownGood("node"); err != nil || got != nil {
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
