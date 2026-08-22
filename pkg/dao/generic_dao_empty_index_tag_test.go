// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package dao

import (
	"testing"

	qrdbErrors "github.com/jeremyhahn/go-qrdb/pkg/errors"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

type emptyTagMember struct {
	ID   uint64 `json:"id"`
	Team string `json:"team" index:""`
}

func (m *emptyTagMember) EntityID() uint64      { return m.ID }
func (m *emptyTagMember) SetEntityID(id uint64) { m.ID = id }

type untaggedMember struct {
	ID   uint64 `json:"id"`
	Team string `json:"team"`
}

func (m *untaggedMember) EntityID() uint64      { return m.ID }
func (m *untaggedMember) SetEntityID(id uint64) { m.ID = id }

// Construction must fail on `index:""`, at the only moment anyone would notice.
//
// Rejecting the tag in ParseIndexTags is not enough on its own: New bound the
// parse error and then read it only as a CONDITION, so a malformed tag skipped
// index registration and returned a perfectly working DAO with none of the
// indexes the author wrote. Fixing the tag check without this would have moved
// the silence from the tag to the constructor (go-qrdb#57).
func TestNew_AnEmptyIndexTagFailsConstruction(t *testing.T) {
	_, err := New(kvstore.NewMemoryKVStore(), "member",
		func() *emptyTagMember { return &emptyTagMember{} })
	if err == nil {
		t.Fatal("dao.New accepted a field tagged `index:\"\"`; the index is never " +
			"created and never advertised, so every query on it is a full scan " +
			"forever with no error to notice")
	}
	if got := qrdbErrors.Code(err); got != qrdbErrors.ErrInvalidInput {
		t.Fatalf("error code = %v, want %v", got, qrdbErrors.ErrInvalidInput)
	}
}

// A field with no index tag at all still constructs cleanly — the case that
// makes the rejection a distinction rather than a blanket refusal.
func TestNew_AnUntaggedFieldStillConstructs(t *testing.T) {
	if _, err := New(kvstore.NewMemoryKVStore(), "member",
		func() *untaggedMember { return &untaggedMember{} }); err != nil {
		t.Fatalf("dao.New on a struct with no index tags: %v", err)
	}
}
