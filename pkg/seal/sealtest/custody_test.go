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

package sealtest

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// shamirBarrier returns a 2-of-3 Shamir barrier with c installed, initialized
// and therefore unsealed.
func shamirBarrier(t *testing.T, c *Custody) *seal.StorageBarrier {
	t.Helper()
	shamirStrategy, err := seal.NewShamirStrategy(2, 3)
	if err != nil {
		t.Fatal(err)
	}
	b, err := seal.NewStorageBarrier(slog.New(slog.NewTextHandler(io.Discard, nil)), seal.NewMemoryBackend(),
		seal.BarrierConfig{RootKeyPath: "root-key", Shamir: &seal.ShamirConfig{Threshold: 2, TotalShares: 3}}, shamirStrategy)
	if err != nil {
		t.Fatal(err)
	}
	c.Install(b)
	if _, err := b.InitializeShamir(context.Background(), ""); err != nil {
		t.Fatalf("InitializeShamir: %v", err)
	}
	return b
}

func TestCustody_SealAndUnsealThroughQuorums(t *testing.T) {
	ctx := context.Background()
	c := NewCustody("alice", "bob", "carol")
	b := shamirBarrier(t, c)
	for _, h := range []string{"alice", "bob", "carol"} {
		if c.Distributor.Share(seal.DomainUnseal, h) == "" {
			t.Fatalf("%s holds no share", h)
		}
	}

	if out, err := c.Quorum(ctx, b, seal.DomainUnseal, seal.OpSeal, "alice", "carol"); err != nil || !out.Complete || !b.IsSealed() {
		t.Fatalf("seal quorum = %+v, %v; sealed=%v", out, err, b.IsSealed())
	}
	if out, err := c.Quorum(ctx, b, seal.DomainUnseal, seal.OpUnseal, "bob", "carol"); err != nil || !out.Complete || b.IsSealed() {
		t.Fatalf("unseal quorum = %+v, %v; sealed=%v", out, err, b.IsSealed())
	}
}

func TestCustody_RevokedHolderDropsOut(t *testing.T) {
	ctx := context.Background()
	c := NewCustody("alice", "bob", "carol", "dave")
	shamirStrategy, err := seal.NewShamirStrategy(2, 4)
	if err != nil {
		t.Fatal(err)
	}
	b, err := seal.NewStorageBarrier(slog.New(slog.NewTextHandler(io.Discard, nil)), seal.NewMemoryBackend(),
		seal.BarrierConfig{RootKeyPath: "root-key", Shamir: &seal.ShamirConfig{Threshold: 2, TotalShares: 4}}, shamirStrategy)
	if err != nil {
		t.Fatal(err)
	}
	c.Install(b)
	if _, err := b.InitializeShamir(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := b.StartRekey(ctx, seal.DomainUnseal, seal.RekeyParams{Threshold: 2, Remove: []string{"dave"}}); err != nil {
		t.Fatalf("StartRekey: %v", err)
	}
	if out, err := c.Quorum(ctx, b, seal.DomainUnseal, seal.OpRekey, "alice", "bob"); err != nil || !out.Complete {
		t.Fatalf("rekey quorum = %+v, %v", out, err)
	}
	roster, _, err := b.HolderRoster(ctx, seal.DomainUnseal)
	if err != nil {
		t.Fatal(err)
	}
	if roster.Total != 3 {
		t.Fatalf("roster after revocation has %d holders, want 3", roster.Total)
	}
	if _, err := c.Distributor.PrepareRoster(ctx, seal.DomainUnseal, 4, "", nil); !errors.Is(err, ErrTooFewHolders) {
		t.Fatalf("PrepareRoster beyond the remaining holders = %v, want ErrTooFewHolders", err)
	}
}

func TestVerifier_ProofIsSingleUseAndBound(t *testing.T) {
	ctx := context.Background()
	v := NewVerifier()
	roster := seal.NewHolderRoster(seal.DomainUnseal, "", 2)
	roster.Holders = []seal.RosterHolder{{Username: "alice", ShareIndex: 1, Designated: "mtls", Factors: []seal.RosterFactor{{Name: "mtls"}}}}
	digest := []byte("digest")

	proof := v.Issue(seal.DomainUnseal, seal.OpUnseal, "alice", digest)
	if err := v.VerifyHolder(ctx, seal.DomainUnseal, seal.OpUnseal, "alice", roster, digest, proof); err != nil {
		t.Fatalf("first use: %v", err)
	}
	if err := v.VerifyHolder(ctx, seal.DomainUnseal, seal.OpUnseal, "alice", roster, digest, proof); !errors.Is(err, ErrProof) {
		t.Fatalf("replay = %v, want ErrProof", err)
	}
	other := v.Issue(seal.DomainUnseal, seal.OpUnseal, "alice", digest)
	if err := v.VerifyHolder(ctx, seal.DomainUnseal, seal.OpSeal, "alice", roster, digest, other); !errors.Is(err, ErrProof) {
		t.Fatalf("proof for another op = %v, want ErrProof", err)
	}
}
