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

//go:build !integration && codec_json && raft

package node

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/dao"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// storageUser declares every index kind the DAO promises: Email is unique,
// Username is unique and case-insensitive, Nickname is sparse (omitempty, so
// a record that never sets it omits the field), and Payload is an unindexed
// byte slice.
type storageUser struct {
	ID       uint64 `json:"id"`
	Email    string `json:"email"              index:"unique"`
	Username string `json:"username"           index:"unique,case_insensitive"`
	Nickname string `json:"nickname,omitempty" index:"true,sparse"`
	Payload  []byte `json:"payload,omitempty"`
}

func (u *storageUser) EntityID() uint64      { return u.ID }
func (u *storageUser) SetEntityID(id uint64) { u.ID = id }

func newStorageUser() *storageUser { return &storageUser{} }

// newStorageUserDAO builds a DAO over the system tenant's store on a
// standalone node, whose engine maintains the declared indexes, so each
// assertion below is answered by the real index rather than a double.
func newStorageUserDAO(ctx context.Context, t *testing.T) dao.GenericDAO[*storageUser] {
	t.Helper()
	store := resolveStore(ctx, t, resolverStoreCase{
		name:         "system_tenant",
		tenantID:     tenantid.System,
		setup:        func(context.Context, *testing.T, *Node) {},
		indexCapable: true,
	})
	d, err := dao.New(store, "storage-user", newStorageUser)
	require.NoError(t, err, "dao.New over the system tenant's store")
	return d
}

func TestDAOEntityStorage_UniqueIndexRefusesADuplicate(t *testing.T) {
	ctx := context.Background()
	d := newStorageUserDAO(ctx, t)

	bob := &storageUser{Email: "shared@example.com", Username: "Bob"}
	require.NoError(t, d.Save(ctx, bob))
	carol := &storageUser{Email: "shared@example.com", Username: "Carol"}
	require.Error(t, d.Save(ctx, carol), "a second entity with bob's Email must be refused")

	matches, err := d.QueryByIndex(ctx, "Email", "shared@example.com")
	require.NoError(t, err)
	require.Len(t, matches, 1, "the refused save must not have indexed a second entity")
	require.Equal(t, bob.ID, matches[0].ID)
	require.Equal(t, "Bob", matches[0].Username)
}

func TestDAOEntityStorage_UniqueIndexAllowsUpdatingTheSameEntity(t *testing.T) {
	ctx := context.Background()
	d := newStorageUserDAO(ctx, t)

	bob := &storageUser{Email: "bob@example.com", Username: "Bob"}
	require.NoError(t, d.Save(ctx, bob))
	bob.Nickname = "Bobby"
	require.NoError(t, d.Save(ctx, bob), "re-saving the holder of a unique value is an update, not a collision")

	got, err := d.Get(ctx, bob.ID)
	require.NoError(t, err)
	require.Equal(t, "Bobby", got.Nickname)
}

func TestDAOEntityStorage_CaseInsensitiveUniqueIndexCollides(t *testing.T) {
	ctx := context.Background()
	d := newStorageUserDAO(ctx, t)

	bob := &storageUser{Email: "bob@example.com", Username: "Bob"}
	require.NoError(t, d.Save(ctx, bob))
	dave := &storageUser{Email: "dave@example.com", Username: "BOB"}
	require.Error(t, d.Save(ctx, dave), "Username BOB case-folds to bob's Bob and must be refused")

	_, err := d.Get(ctx, dave.ID)
	require.True(t, dave.ID == 0 || dao.IsNotFound(err), "the refused save must store nothing, got err=%v", err)
}

func TestDAOEntityStorage_CaseInsensitiveIndexAnswersADifferentlyCasedProbe(t *testing.T) {
	ctx := context.Background()
	d := newStorageUserDAO(ctx, t)

	bob := &storageUser{Email: "bob@example.com", Username: "Bob"}
	require.NoError(t, d.Save(ctx, bob))

	for _, probe := range []string{"BOB", "bob", "Bob"} {
		found, err := d.QueryOneByIndex(ctx, "Username", probe)
		require.NoErrorf(t, err, "QueryOneByIndex(Username, %s)", probe)
		require.Equalf(t, bob.ID, found.ID, "QueryOneByIndex(Username, %s)", probe)
	}
}

func TestDAOEntityStorage_SparseIndexOmitsAnUnsetField(t *testing.T) {
	ctx := context.Background()
	d := newStorageUserDAO(ctx, t)

	withNick := &storageUser{Email: "nick1@example.com", Username: "Nick1", Nickname: "Nicky"}
	withoutNick := &storageUser{Email: "nick2@example.com", Username: "Nick2"}
	require.NoError(t, d.Save(ctx, withNick))
	require.NoError(t, d.Save(ctx, withoutNick))

	matches, err := d.QueryByIndex(ctx, "Nickname", "Nicky")
	require.NoError(t, err)
	require.Len(t, matches, 1)
	require.Equal(t, withNick.ID, matches[0].ID)
}

func TestDAOEntityStorage_SparseIndexDropsAClearedField(t *testing.T) {
	ctx := context.Background()
	d := newStorageUserDAO(ctx, t)

	u := &storageUser{Email: "nick@example.com", Username: "Nick", Nickname: "Nicky"}
	require.NoError(t, d.Save(ctx, u))
	u.Nickname = ""
	require.NoError(t, d.Save(ctx, u))

	matches, err := d.QueryByIndex(ctx, "Nickname", "Nicky")
	require.NoError(t, err)
	require.Empty(t, matches, "a cleared sparse field must leave the index")
}

func TestDAOEntityStorage_BinaryAndLargeValuesRoundTrip(t *testing.T) {
	ctx := context.Background()
	d := newStorageUserDAO(ctx, t)

	binary := make([]byte, 256)
	for i := range binary {
		binary[i] = byte(i)
	}
	large := make([]byte, 256*1024)
	for i := range large {
		large[i] = byte(i % 251)
	}

	for _, tc := range []struct {
		name    string
		payload []byte
	}{
		{name: "binary-all-byte-values", payload: binary},
		{name: "large-256KiB", payload: large},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := &storageUser{Email: tc.name + "@example.com", Username: tc.name, Payload: tc.payload}
			require.NoError(t, d.Save(ctx, u))
			got, err := d.Get(ctx, u.ID)
			require.NoError(t, err)
			require.Truef(t, bytes.Equal(got.Payload, tc.payload),
				"payload round-trip: got %d bytes, want %d", len(got.Payload), len(tc.payload))
		})
	}
}

func TestDAOEntityStorage_EmptyPayloadRoundTripsAsEmpty(t *testing.T) {
	ctx := context.Background()
	d := newStorageUserDAO(ctx, t)

	u := &storageUser{Email: "empty@example.com", Username: "Empty"}
	require.NoError(t, d.Save(ctx, u))
	got, err := d.Get(ctx, u.ID)
	require.NoError(t, err)
	require.Empty(t, got.Payload)
}
