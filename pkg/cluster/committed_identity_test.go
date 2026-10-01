// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cluster

import (
	"context"
	"errors"
	"strings"
	"testing"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
)

// fakeMemberships answers SyncGetShardMembership from a fixed table, and
// fails for a shard it holds no entry for with quicraft.ErrShardNotFound — as
// go-quicraft does for a shard this host has not loaded. shardErrs fails a
// read of one shard; err fails every read.
type fakeMemberships struct {
	shards    map[uint64]*quicraft.Membership
	shardErrs map[uint64]error
	err       error
}

func (f *fakeMemberships) SyncGetShardMembership(_ context.Context, shardID uint64) (*quicraft.Membership, error) {
	if f.err != nil {
		return nil, f.err
	}
	if err := f.shardErrs[shardID]; err != nil {
		return nil, err
	}
	m, ok := f.shards[shardID]
	if !ok {
		return nil, quicraft.ErrShardNotFound
	}
	return m, nil
}

var _ MembershipReader = (*fakeMemberships)(nil)

const (
	testSystemShard = 1
	testDedicated   = 1000
	// testUnready is loaded but cannot be read (no leader yet, say).
	testUnready = 3000
)

var (
	idHostA          = "spki:" + strings.Repeat("aa", 32)
	idHostB          = "spki:" + strings.Repeat("bb", 32)
	idStranger       = "spki:" + strings.Repeat("cc", 32)
	idDedicatedFirst = "spki:" + strings.Repeat("dd", 32)
)

// cluster models a two-host system shard (replicas 1 and 2 at a.example and
// b.example, identities A and B) and a dedicated shard whose replica 7 is
// already committed.
func testCluster() *fakeMemberships {
	return &fakeMemberships{shards: map[uint64]*quicraft.Membership{
		testSystemShard: {
			Nodes:      map[uint64]string{1: "a.example:1", 2: "b.example:1"},
			Identities: map[uint64]string{1: idHostA, 2: idHostB},
		},
		testDedicated: {
			Nodes:      map[uint64]string{7: "a.example:1"},
			Identities: map[uint64]string{7: idDedicatedFirst},
		},
	}, shardErrs: map[uint64]error{testUnready: quicraft.ErrShardNotReady}}
}

func TestIdentityResolver_Resolve(t *testing.T) {
	tests := []struct {
		name     string
		shard    uint64
		replica  uint64
		addr     string
		override string
		required bool
		want     string
		wantErr  any
	}{
		{name: "system shard replica by committed id", shard: testSystemShard, replica: 2, addr: "b.example:1", required: true, want: idHostB},
		{name: "dedicated replica already committed in its shard", shard: testDedicated, replica: 7, addr: "a.example:1", required: true, want: idDedicatedFirst},
		{name: "dedicated replica on a host committed in the system shard", shard: testDedicated, replica: 8, addr: "b.example:1", required: true, want: idHostB},
		{name: "shard not yet loaded falls back to the system shard", shard: 2000, replica: 1, addr: "a.example:1", required: true, want: idHostA},
		{name: "override matching the committed identity", shard: testSystemShard, replica: 2, addr: "b.example:1", override: idHostB, required: true, want: idHostB},
		{name: "override for a brand-new node", shard: testSystemShard, replica: 4, addr: "d.example:1", override: idStranger, required: true, want: idStranger},
		{name: "override contradicting the committed identity", shard: testSystemShard, replica: 2, addr: "b.example:1", override: idStranger, required: true, wantErr: new(*MemberIdentityConflictError)},
		{name: "malformed override", shard: testSystemShard, replica: 4, addr: "d.example:1", override: "spki:nope", required: true, wantErr: new(*MemberIdentityFormatError)},
		{name: "brand-new node without override", shard: testSystemShard, replica: 4, addr: "d.example:1", required: true, wantErr: new(*MemberIdentityUnknownError)},
		{name: "unknown host of a dedicated shard", shard: testDedicated, replica: 9, addr: "d.example:1", required: true, wantErr: new(*MemberIdentityUnknownError)},
		{name: "transport-less host needs none", shard: testSystemShard, replica: 4, addr: "d.example:1", required: false, want: ""},
		{name: "dedicated override contradicting the identity found by address", shard: testDedicated, replica: 8, addr: "b.example:1", override: idStranger, required: true, wantErr: new(*MemberIdentityConflictError)},
		{name: "system shard new replica at a committed host resolves to that host", shard: testSystemShard, replica: 5, addr: "b.example:1", required: true, want: idHostB},
		{name: "system shard override contradicting the identity found by address", shard: testSystemShard, replica: 5, addr: "b.example:1", override: idStranger, required: true, wantErr: new(*MemberIdentityConflictError)},
		{name: "target shard unreadable for another reason is a read error", shard: testUnready, replica: 1, addr: "a.example:1", required: true, wantErr: new(*MembershipReadError)},
		{name: "dedicated replica committed at another address", shard: testDedicated, replica: 7, addr: "b.example:1", required: true, wantErr: new(*MemberAddressMismatchError)},
		{name: "system replica committed at another address", shard: testSystemShard, replica: 2, addr: "a.example:1", required: true, wantErr: new(*MemberAddressMismatchError)},
		{name: "system replica at another address even with a matching override", shard: testSystemShard, replica: 2, addr: "a.example:1", override: idHostB, required: true, wantErr: new(*MemberAddressMismatchError)},
		{name: "transport-less host keeps an override", shard: testSystemShard, replica: 4, addr: "d.example:1", override: idStranger, required: false, want: idStranger},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := NewIdentityResolver(testCluster(), testSystemShard, tc.required)
			got, err := r.Resolve(context.Background(), tc.shard, tc.replica, tc.addr, tc.override)
			if tc.wantErr != nil {
				if !errors.As(err, tc.wantErr) {
					t.Fatalf("Resolve = (%q, %v), want %T", got, err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("Resolve = (%q, %v), want %q", got, err, tc.want)
			}
		})
	}
}

// TestIdentityResolver_UnreadableTargetShardNamesIt pins that a target shard
// that exists but cannot be read is reported as that shard's read failure —
// not resolved from the host's system-shard identity, which would skip an
// identity the target shard may already have committed for the replica.
func TestIdentityResolver_UnreadableTargetShardNamesIt(t *testing.T) {
	r := NewIdentityResolver(testCluster(), testSystemShard, true)
	_, err := r.Resolve(context.Background(), testUnready, 1, "a.example:1", "")
	var readErr *MembershipReadError
	if !errors.As(err, &readErr) || readErr.ShardID != testUnready || !errors.Is(err, quicraft.ErrShardNotReady) {
		t.Fatalf("err = %v, want *MembershipReadError for shard %d wrapping ErrShardNotReady", err, testUnready)
	}
}

// TestIdentityResolver_AddressMatches pins that every replica the system
// shard places at an address is consulted: replicas that agree resolve to
// their identity, replicas that disagree are refused as ambiguous.
func TestIdentityResolver_AddressMatches(t *testing.T) {
	for _, tc := range []struct {
		name       string
		identities map[uint64]string
		want       string
		ambiguous  bool
	}{
		{name: "agreeing matches", identities: map[uint64]string{1: idHostA, 2: idHostB, 3: idHostB}, want: idHostB},
		{name: "a match without an identity defers to the one with it", identities: map[uint64]string{1: idHostA, 2: idHostB}, want: idHostB},
		{name: "disagreeing matches", identities: map[uint64]string{1: idHostA, 2: idHostB, 3: idStranger}, ambiguous: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &fakeMemberships{shards: map[uint64]*quicraft.Membership{
				testSystemShard: {
					Nodes:      map[uint64]string{1: "a.example:1", 2: "b.example:1"},
					Observers:  map[uint64]string{3: "b.example:1"},
					Identities: tc.identities,
				},
			}}
			got, err := NewIdentityResolver(m, testSystemShard, true).Resolve(context.Background(), testDedicated, 9, "b.example:1", "")
			if tc.ambiguous {
				var amb *MemberIdentityAmbiguousError
				if !errors.As(err, &amb) || amb.Address != "b.example:1" || len(amb.Identities) != 2 {
					t.Fatalf("Resolve = (%q, %v), want *MemberIdentityAmbiguousError naming both identities", got, err)
				}
				if !strings.Contains(err.Error(), idHostB) || !strings.Contains(err.Error(), idStranger) {
					t.Fatalf("ambiguity error must name the identities: %q", err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("Resolve = (%q, %v), want %q", got, err, tc.want)
			}
		})
	}
}

func TestIdentityResolver_UnreadableSystemMembership(t *testing.T) {
	cause := errors.New("no leader")
	r := NewIdentityResolver(&fakeMemberships{err: cause}, testSystemShard, true)
	_, err := r.Resolve(context.Background(), testSystemShard, 2, "b.example:1", "")
	var readErr *MembershipReadError
	if !errors.As(err, &readErr) || !errors.Is(err, cause) || readErr.ShardID != testSystemShard {
		t.Fatalf("err = %v, want *MembershipReadError wrapping the cause", err)
	}
}

func TestCommittedIdentityErrors(t *testing.T) {
	unknown := (&MemberIdentityUnknownError{ShardID: 1, ReplicaID: 4, Address: "d.example:1"}).Error()
	if !strings.Contains(unknown, "replica 4") || !strings.Contains(unknown, "qrdbd node-identity --cert") {
		t.Fatalf("unknown-identity error must name the replica and how to get the identity: %q", unknown)
	}
	conflict := (&MemberIdentityConflictError{ShardID: 1, ReplicaID: 2, Committed: idHostB, Override: idStranger}).Error()
	if !strings.Contains(conflict, idHostB) || !strings.Contains(conflict, idStranger) {
		t.Fatalf("conflict error must name both identities: %q", conflict)
	}
	mismatch := (&MemberAddressMismatchError{ShardID: 1, ReplicaID: 2, Committed: "b.example:1", Address: "a.example:1"}).Error()
	if !strings.Contains(mismatch, "replica 2") || !strings.Contains(mismatch, "b.example:1") || !strings.Contains(mismatch, "a.example:1") {
		t.Fatalf("address-mismatch error must name the replica and both addresses: %q", mismatch)
	}
	cause := errors.New("closed")
	read := &MembershipReadError{ShardID: 1, Err: cause}
	if !errors.Is(read, cause) || !strings.Contains(read.Error(), "shard 1") {
		t.Fatalf("read error = %q", read.Error())
	}
}
