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

// TestQuorum_StaleRosterSubmissionNeverDestroysANewerRound: a submission
// reads the domain's roster before it takes the quorum lock. If a rekey
// replaced the roster in between and a round has opened on the new one, the
// late submission's digest is the stale one. It used to replace the newer
// round as if that round were the stale one, destroying the shares holders
// had already counted. It is now refused as retryable, and the newer round
// keeps its shares.
func TestQuorum_StaleRosterSubmissionNeverDestroysANewerRound(t *testing.T) {
	ctx := context.Background()
	f := newHolderFixture(t, 2, 3)

	// The submission's read, taken before the rekey.
	_, staleRoster, staleDigest, err := f.b.loadWrapped(ctx, DomainUnseal)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := f.b.StartRekey(ctx, DomainUnseal, RekeyParams{Threshold: 2}); err != nil {
		t.Fatal(err)
	}
	if o := f.quorum(DomainUnseal, OpRekey, "h1", "h2"); !o.Complete {
		t.Fatalf("rekey incomplete: %+v", o)
	}
	// A seal round opens on the new roster with h1's share.
	if o := f.quorum(DomainUnseal, OpSeal, "h1"); o.Submitted != 1 {
		t.Fatalf("seal round: %+v", o)
	}

	// The late submission, carrying the stale read.
	_, err = f.b.submitAgainst(ctx, &HolderSubmission{
		Domain: DomainUnseal, Op: OpSeal, Holder: "h3",
		Proof: f.verifier.issue(DomainUnseal, OpSeal, "h3", staleDigest),
		Share: []byte(f.dist.share(DomainUnseal, "h3")),
	}, staleRoster, staleDigest)
	var stale *StaleRosterError
	if !errors.As(err, &stale) || stale.Domain != DomainUnseal {
		t.Fatalf("stale submission = %v, want *StaleRosterError", err)
	}

	// h1's share still counts: one more holder completes the seal.
	o, err := f.submit(DomainUnseal, OpSeal, "h2")
	if err != nil {
		t.Fatal(err)
	}
	if !o.Complete || !f.b.IsSealed() {
		t.Fatalf("the newer round lost h1's share: %+v, sealed=%v", o, f.b.IsSealed())
	}
}

// TestQuorum_StaleRoundIsStillReplaced: the opposite order is unchanged. A
// round left over from a roster since replaced is discarded by a submission
// that read the current roster.
func TestQuorum_StaleRoundIsStillReplaced(t *testing.T) {
	ctx := context.Background()
	f := newHolderFixture(t, 2, 3)
	if o := f.quorum(DomainUnseal, OpRootToken, "h1"); o.Submitted != 1 {
		t.Fatalf("root-token round: %+v", o)
	}
	if _, err := f.b.StartRekey(ctx, DomainUnseal, RekeyParams{Threshold: 2}); err != nil {
		t.Fatal(err)
	}
	if o := f.quorum(DomainUnseal, OpRekey, "h1", "h2"); !o.Complete {
		t.Fatalf("rekey incomplete: %+v", o)
	}
	o := f.quorum(DomainUnseal, OpRootToken, "h1")
	if o.Submitted != 1 {
		t.Fatalf("h1 counted against the old round: %+v", o)
	}
}
