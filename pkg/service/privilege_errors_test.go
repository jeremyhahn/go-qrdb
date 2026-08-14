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

package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrivilegeErrors_Messages(t *testing.T) {
	cause := errors.New("boom")

	tests := []struct {
		name     string
		err      error
		contains []string
	}{
		{
			name:     "empty database",
			err:      &PrivilegeEmptyDatabaseError{},
			contains: []string{"service:", "database"},
		},
		{
			name:     "invalid grantee",
			err:      &PrivilegeInvalidGranteeError{Grantee: "a/b", Reason: "bad"},
			contains: []string{`"a/b"`, "bad"},
		},
		{
			name:     "no grants",
			err:      &PrivilegeNoGrantsError{},
			contains: []string{"at least one grant"},
		},
		{
			name:     "column without table",
			err:      &PrivilegeColumnWithoutTableError{Column: "total"},
			contains: []string{`"total"`, "without a table"},
		},
		{
			name:     "unknown privilege name",
			err:      &PrivilegeUnknownNameError{Name: "TELEPORT"},
			contains: []string{`"TELEPORT"`},
		},
		{
			name:     "metadata",
			err:      &PrivilegeMetadataError{Op: "unmarshal", Err: cause},
			contains: []string{"unmarshal", "boom"},
		},
		{
			name:     "conflict",
			err:      &PrivilegeConflictError{Database: "shop", Grantee: "alice@%", Err: cause},
			contains: []string{"shop", "alice@%"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			msg := tc.err.Error()
			require.NotEmpty(t, msg)
			assert.True(t, strings.HasPrefix(msg, "service: "),
				"every error must identify its origin package, got %q", msg)
			for _, want := range tc.contains {
				assert.Contains(t, msg, want)
			}
		})
	}
}

func TestPrivilegeErrors_Unwrap(t *testing.T) {
	cause := errors.New("boom")

	assert.ErrorIs(t, &PrivilegeMetadataError{Op: "read", Err: cause}, cause)
	assert.ErrorIs(t, &PrivilegeConflictError{Database: "shop", Grantee: "alice@%", Err: cause}, cause)
}

// TestPrivilegeSet_AnyCoversEveryTier pins that Any consults all three tiers,
// which is what CheckDatabase relies on to decide a database is reachable.
func TestPrivilegeSet_AnyCoversEveryTier(t *testing.T) {
	tests := []struct {
		name  string
		grant PrivilegeGrant
	}{
		{"database tier", PrivilegeGrant{Privileges: PrivilegeSelect}},
		{"table tier", PrivilegeGrant{Table: "orders", Privileges: PrivilegeSelect}},
		{"column tier", PrivilegeGrant{Table: "orders", Column: "id", Privileges: PrivilegeSelect}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			set := newPrivilegeSet("shop", "alice@%")
			set.grant(tc.grant)
			assert.Equal(t, PrivilegeSelect, set.Any())
		})
	}
}

// TestPrivilegeSet_RevokeColumnsOnAbsentTable pins the no-op path, which a
// REVOKE naming a table the grantee never held reaches.
func TestPrivilegeSet_RevokeColumnsOnAbsentTable(t *testing.T) {
	set := newPrivilegeSet("shop", "alice@%")
	set.grant(PrivilegeGrant{Table: "orders", Column: "id", Privileges: PrivilegeSelect})

	set.revokeColumns("customers", "", PrivilegeSelect)
	assert.Equal(t, PrivilegeSelect, set.Column("orders", "id"))

	set.revokeColumns("orders", "other", PrivilegeSelect)
	assert.Equal(t, PrivilegeSelect, set.Column("orders", "id"),
		"revoking a column that is not held must leave the held one alone")
}

// TestDecodePrivilegeSet_EmptyValue pins that a zero-length stored value
// decodes as an empty record rather than as a JSON error, which is what a
// tombstoned key can legitimately read back as.
func TestDecodePrivilegeSet_EmptyValue(t *testing.T) {
	set, err := decodePrivilegeSet(nil, "shop", "alice@%")
	require.NoError(t, err)
	assert.True(t, set.IsEmpty())
	assert.Equal(t, "shop", set.Database)
	assert.Equal(t, "alice@%", set.Grantee)
}

// stagedFailStore fails the Nth Get made against it and serves every other
// call from the wrapped store. It exists so that Resolve's staged read can be
// interrupted at a chosen stage -- latch, then grantee epoch, then record --
// which is the only way to reach the later stages' error paths.
type stagedFailStore struct {
	*fakeCatalogStore
	failAt int
	calls  int
	err    error
}

func (s *stagedFailStore) Get(ctx context.Context, key string) ([]byte, error) {
	s.calls++
	if s.calls == s.failAt {
		return nil, s.err
	}
	return s.fakeCatalogStore.Get(ctx, key)
}

// newStagedFailService builds a PrivilegeService whose failAt-th Get fails.
func newStagedFailService(failAt int, err error) (*PrivilegeService, *stagedFailStore) {
	store := newFakeCatalogStore()
	staged := &stagedFailStore{fakeCatalogStore: store, failAt: failAt, err: err}
	return NewPrivilegeService(staged, store, store), staged
}

// TestPrivilegeService_ResolvePropagatesStagedReadErrors pins each stage of
// Resolve's staged read: the latch, then the grantee's epoch (only reached
// once the database is latched), then the record itself.
func TestPrivilegeService_ResolvePropagatesStagedReadErrors(t *testing.T) {
	tests := []struct {
		name   string
		failAt int
	}{
		{"latch read", 1},
		{"grantee epoch read", 2},
		{"record read", 3},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sentinel := errors.New("read failed at stage")
			svc, staged := newStagedFailService(0, sentinel)
			ctx := context.Background()

			grantSelect(t, svc, "shop", "alice@%", "orders")

			// Arm the injector only now, so the setup grant's own reads do
			// not consume the budget.
			staged.calls = 0
			staged.failAt = tc.failAt

			_, err := svc.Resolve(ctx, &ResolvePrivilegesRequest{Database: "shop", Grantee: "alice@%"})
			var metadata *PrivilegeMetadataError
			require.ErrorAs(t, err, &metadata)
			assert.ErrorIs(t, err, sentinel)
		})
	}
}
