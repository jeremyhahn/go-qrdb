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
	"context"
	"errors"
	"testing"
)

// TestInitialize_RefusesShamirWithoutHolders proves a Shamir barrier cannot
// be initialized through the credential entry points. Initialize and
// InitializeWithStrategy would write a Shamir envelope with no wrapped key
// and no roster: the barrier would run until it was first sealed and then
// never open again, because a Shamir barrier refuses credential unseal and a
// quorum needs a roster. Only InitializeShamir, which distributes to the
// enrolled holders, may initialize one.
func TestInitialize_RefusesShamirWithoutHolders(t *testing.T) {
	ctx := context.Background()
	creds, err := CredentialsFor(context.Background(), []byte("a passphrase that must not seed a shamir barrier"))
	if err != nil {
		t.Fatal(err)
	}
	defer creds.Secret.Destroy()

	for name, init := range map[string]func(b *StorageBarrier, s *ShamirStrategy) error{
		"Initialize": func(b *StorageBarrier, _ *ShamirStrategy) error {
			return b.Initialize(ctx, creds)
		},
		"InitializeWithStrategy": func(b *StorageBarrier, s *ShamirStrategy) error {
			return b.InitializeWithStrategy(ctx, s, creds)
		},
	} {
		t.Run(name, func(t *testing.T) {
			shamirStrategy, err := NewShamirStrategy(2, 3)
			if err != nil {
				t.Fatal(err)
			}
			b, err := NewStorageBarrier(testLogger(), NewMemoryBackend(), BarrierConfig{
				RootKeyPath: "root-key",
				Shamir:      &ShamirConfig{Threshold: 2, TotalShares: 3},
			}, shamirStrategy)
			if err != nil {
				t.Fatal(err)
			}
			if err := init(b, shamirStrategy); !errors.Is(err, ErrShamirInitializeRequiresHolders) {
				t.Fatalf("err = %v, want ErrShamirInitializeRequiresHolders", err)
			}
			if exists, err := b.base.Exists(ctx, b.config.RootKeyPath); err != nil || exists {
				t.Fatalf("a root key was written (exists=%v, err=%v)", exists, err)
			}
			if !b.IsSealed() {
				t.Fatal("the refused barrier is unsealed")
			}
		})
	}
}

// TestInitializeShamir_StillInitializes proves the holder path is unaffected:
// InitializeShamir initializes the barrier and distributes to its holders.
func TestInitializeShamir_StillInitializes(t *testing.T) {
	f := newHolderFixture(t, 2, 3)
	if f.b.IsSealed() {
		t.Fatal("barrier sealed after InitializeShamir")
	}
	if _, _, err := f.b.HolderRoster(context.Background(), DomainUnseal); err != nil {
		t.Fatalf("no roster after InitializeShamir: %v", err)
	}
}
