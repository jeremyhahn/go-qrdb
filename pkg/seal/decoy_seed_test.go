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

package seal

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

// TestDecoySeed_PersistsPerDomain: the seed is created once, read back by
// another barrier over the same storage, and differs between domains.
func TestDecoySeed_PersistsPerDomain(t *testing.T) {
	ctx := context.Background()
	backend := NewMemoryBackend()
	first := &StorageBarrier{base: backend, config: BarrierConfig{RootKeyPath: "root-key"}}
	seed, err := first.DecoySeed(ctx, DomainUnseal)
	if err != nil || len(seed) != decoySeedSize {
		t.Fatalf("DecoySeed = %x, %v", seed, err)
	}
	second := &StorageBarrier{base: backend, config: BarrierConfig{RootKeyPath: "root-key"}}
	again, err := second.DecoySeed(ctx, DomainUnseal)
	if err != nil || !bytes.Equal(seed, again) {
		t.Fatalf("the seed did not persist: %x vs %x (%v)", seed, again, err)
	}
	recovery, err := second.DecoySeed(ctx, DomainRecovery)
	if err != nil || bytes.Equal(seed, recovery) {
		t.Fatalf("the recovery domain shares the unseal seed (%v)", err)
	}
}

// TestDecoySeed_Refusals: an invalid domain and a corrupt seed are refused.
func TestDecoySeed_Refusals(t *testing.T) {
	ctx := context.Background()
	backend := NewMemoryBackend()
	b := &StorageBarrier{base: backend, config: BarrierConfig{RootKeyPath: "root-key"}}
	var invalid *QuorumOpInvalidError
	if _, err := b.DecoySeed(ctx, QuorumDomain("nope")); !errors.As(err, &invalid) {
		t.Fatalf("invalid domain = %v", err)
	}
	if err := backend.Put(ctx, "root-key.unseal"+decoySeedPathSuffix, []byte("short")); err != nil {
		t.Fatal(err)
	}
	var size *DecoySeedSizeError
	if _, err := b.DecoySeed(ctx, DomainUnseal); !errors.As(err, &size) || size.Error() == "" {
		t.Fatalf("corrupt seed = %v", err)
	}
}

// racingSeedStore is a barrier base whose seed creation can lose a race:
// when racer is set, another process's seed lands just before this one's
// PutIfAbsent, which then finds the path taken. getErrs fail the reads in
// order (a nil entry lets that read through); putErr fails the create.
type racingSeedStore struct {
	StorageBackend
	racer   []byte
	getErrs []error
	putErr  error
}

func (s *racingSeedStore) Get(ctx context.Context, key string) ([]byte, error) {
	if len(s.getErrs) > 0 {
		err := s.getErrs[0]
		s.getErrs = s.getErrs[1:]
		if err != nil {
			return nil, err
		}
	}
	return s.StorageBackend.Get(ctx, key)
}

func (s *racingSeedStore) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	if s.putErr != nil {
		return false, s.putErr
	}
	if s.racer != nil {
		if _, err := s.StorageBackend.PutIfAbsent(ctx, key, s.racer); err != nil {
			return false, err
		}
	}
	return s.StorageBackend.PutIfAbsent(ctx, key, value)
}

// TestDecoySeed_RaceAndStoreFaults: a process that loses the creation race
// adopts the winner's seed; a read that fails for any reason but "absent",
// a create that fails, and a failed read-back after a lost race are returned,
// never answered with a seed of this process's own.
func TestDecoySeed_RaceAndStoreFaults(t *testing.T) {
	ctx := context.Background()
	winner := bytes.Repeat([]byte{7}, decoySeedSize)
	lost := &racingSeedStore{StorageBackend: NewMemoryBackend(), racer: winner}
	b := &StorageBarrier{base: lost, config: BarrierConfig{RootKeyPath: "root-key"}}
	if seed, err := b.DecoySeed(ctx, DomainUnseal); err != nil || !bytes.Equal(seed, winner) {
		t.Fatalf("after a lost race = %x, %v; want the winner's seed", seed, err)
	}

	fault := errors.New("store down")
	for name, store := range map[string]*racingSeedStore{
		"read fails":           {StorageBackend: NewMemoryBackend(), getErrs: []error{fault}},
		"create fails":         {StorageBackend: NewMemoryBackend(), putErr: fault},
		"read-back after race": {StorageBackend: NewMemoryBackend(), racer: winner, getErrs: []error{nil, fault}},
	} {
		b := &StorageBarrier{base: store, config: BarrierConfig{RootKeyPath: "root-key"}}
		if seed, err := b.DecoySeed(ctx, DomainUnseal); !errors.Is(err, fault) || seed != nil {
			t.Errorf("%s: DecoySeed = %x, %v; want the store's fault", name, seed, err)
		}
	}
}
