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
