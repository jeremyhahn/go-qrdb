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

package kvstore

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
)

// memberIndexes is the registration a DAO over a member entity would make:
// one non-unique index on the JSON field "team".
func memberIndexes() []EntityIndex {
	return []EntityIndex{{FieldName: "Team", JSONFieldName: "team"}}
}

// memberValue renders a member record the way a JSON codec would.
func memberValue(name, team string) []byte {
	return fmt.Appendf(nil, `{"name":%q,"team":%q}`, name, team)
}

// keyStrings renders index results as a comparable set of key strings.
func keyStrings(keys [][]byte) []string {
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = string(k)
	}
	return out
}

func TestMemoryKVStore_PutGetDeleteRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryKVStore()

	require.NoError(t, s.Put(ctx, "member/1", memberValue("ana", "alpha")))

	got, err := s.Get(ctx, "member/1")
	require.NoError(t, err)
	require.JSONEq(t, `{"name":"ana","team":"alpha"}`, string(got))

	exists, err := s.Exists(ctx, "member/1")
	require.NoError(t, err)
	require.True(t, exists)

	require.NoError(t, s.Delete(ctx, "member/1"))

	got, err = s.Get(ctx, "member/1")
	require.NoError(t, err)
	require.Nil(t, got, "a missing key reads back as (nil, nil) per the KVStore contract")
}

func TestMemoryKVStore_GetMissingKeyIsNotAnError(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryKVStore()

	got, err := s.Get(ctx, "member/absent")
	require.NoError(t, err)
	require.Nil(t, got)

	exists, err := s.Exists(ctx, "member/absent")
	require.NoError(t, err)
	require.False(t, exists)

	require.NoError(t, s.Delete(ctx, "member/absent"),
		"deleting an absent key is a no-op, not a failure")
}

func TestMemoryKVStore_GetReturnsACopy(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryKVStore()
	require.NoError(t, s.Put(ctx, "member/1", memberValue("ana", "alpha")))

	got, err := s.Get(ctx, "member/1")
	require.NoError(t, err)
	got[0] = 'X'

	again, err := s.Get(ctx, "member/1")
	require.NoError(t, err)
	require.JSONEq(t, `{"name":"ana","team":"alpha"}`, string(again),
		"mutating a returned value must not corrupt the store")
}

func TestMemoryKVStore_ContextCancellationIsHonoured(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := NewMemoryKVStore()

	require.ErrorIs(t, s.Put(ctx, "member/1", memberValue("ana", "alpha")), context.Canceled)

	_, err := s.Get(ctx, "member/1")
	require.ErrorIs(t, err, context.Canceled)

	_, err = s.PutIfAbsent(ctx, "member/1", memberValue("ana", "alpha"))
	require.ErrorIs(t, err, context.Canceled)

	require.ErrorIs(t, s.Delete(ctx, "member/1"), context.Canceled)

	_, err = s.List(ctx, "member/")
	require.ErrorIs(t, err, context.Canceled)

	_, err = s.Exists(ctx, "member/1")
	require.ErrorIs(t, err, context.Canceled)

	require.ErrorIs(t, s.Scan(ctx, "member/", func(string, []byte) error { return nil }),
		context.Canceled)

	_, err = s.QueryIndex(ctx, "member.Team", []byte("alpha"))
	require.ErrorIs(t, err, context.Canceled)

	_, err = s.ScanIndex(ctx, "member.Team", []byte("a"), []byte("z"))
	require.ErrorIs(t, err, context.Canceled)
}

func TestMemoryKVStore_ScanAndListAreOrderedAndPrefixed(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryKVStore()
	require.NoError(t, s.Put(ctx, "member/2", memberValue("ben", "alpha")))
	require.NoError(t, s.Put(ctx, "member/1", memberValue("ana", "alpha")))
	require.NoError(t, s.Put(ctx, "other/1", memberValue("zed", "beta")))

	keys, err := s.List(ctx, "member/")
	require.NoError(t, err)
	require.Equal(t, []string{"member/1", "member/2"}, keys)

	var scanned []string
	require.NoError(t, s.Scan(ctx, "member/", func(key string, _ []byte) error {
		scanned = append(scanned, key)
		return nil
	}))
	require.Equal(t, []string{"member/1", "member/2"}, scanned)
}

func TestMemoryKVStore_ScanStopsOnCallbackError(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryKVStore()
	require.NoError(t, s.Put(ctx, "member/1", memberValue("ana", "alpha")))
	require.NoError(t, s.Put(ctx, "member/2", memberValue("ben", "alpha")))

	sentinel := errors.New("stop")
	visited := 0
	err := s.Scan(ctx, "member/", func(string, []byte) error {
		visited++
		return sentinel
	})
	require.ErrorIs(t, err, sentinel)
	require.Equal(t, 1, visited, "Scan must stop at the first callback error")
}

// TestMemoryKVStore_PutIfAbsentHasExactlyOneWinner is the reason this store
// exists in exported form: a consumer must be able to test create-if-absent
// semantics without booting a node, and the store standing in for a real one
// must not be the degraded check-then-put it is replacing.
func TestMemoryKVStore_PutIfAbsentHasExactlyOneWinner(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryKVStore()

	const writers = 32
	var winners atomic.Int64
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(writers)
	for i := 0; i < writers; i++ {
		go func(i int) {
			defer wg.Done()
			<-start
			stored, err := s.PutIfAbsent(ctx, "member/claim",
				memberValue(fmt.Sprintf("writer-%d", i), "alpha"))
			if err == nil && stored {
				winners.Add(1)
			}
		}(i)
	}
	close(start)
	wg.Wait()

	require.Equal(t, int64(1), winners.Load(),
		"exactly one of %d concurrent PutIfAbsent callers may report stored=true; "+
			"more than one means the check and the write are not atomic", writers)
}

func TestMemoryKVStore_PutIfAbsentLeavesAnExistingValueUnchanged(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryKVStore()
	require.NoError(t, s.Put(ctx, "member/1", memberValue("ana", "alpha")))

	stored, err := s.PutIfAbsent(ctx, "member/1", memberValue("ben", "beta"))
	require.NoError(t, err)
	require.False(t, stored)

	got, err := s.Get(ctx, "member/1")
	require.NoError(t, err)
	require.JSONEq(t, `{"name":"ana","team":"alpha"}`, string(got),
		"a losing PutIfAbsent must leave the store unchanged")
}

// TestMemoryKVStore_IndexIsMaintainedOnWrite is the property the pkg/dao test
// doubles never had: index entries appear because a value was WRITTEN, not
// because a helper inserted them.
func TestMemoryKVStore_IndexIsMaintainedOnWrite(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryKVStore()
	require.NoError(t, s.RegisterEntityIndexes("member", memberIndexes()))

	require.NoError(t, s.Put(ctx, "member/1", memberValue("ana", "alpha")))
	require.NoError(t, s.Put(ctx, "member/2", memberValue("ben", "alpha")))
	require.NoError(t, s.Put(ctx, "member/3", memberValue("cal", "beta")))

	keys, err := s.QueryIndex(ctx, "member.Team", []byte("alpha"))
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"member/1", "member/2"}, keyStrings(keys),
		"QueryIndex must return the keys whose written value carries the field")
}

func TestMemoryKVStore_IndexFollowsAnUpdatedFieldValue(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryKVStore()
	require.NoError(t, s.RegisterEntityIndexes("member", memberIndexes()))
	require.NoError(t, s.Put(ctx, "member/1", memberValue("ana", "alpha")))

	// Move the member to another team.
	require.NoError(t, s.Put(ctx, "member/1", memberValue("ana", "beta")))

	stale, err := s.QueryIndex(ctx, "member.Team", []byte("alpha"))
	require.NoError(t, err)
	require.Empty(t, keyStrings(stale),
		"the previous value's index entry must be removed, or a query returns a "+
			"member who has left the team")

	current, err := s.QueryIndex(ctx, "member.Team", []byte("beta"))
	require.NoError(t, err)
	require.Equal(t, []string{"member/1"}, keyStrings(current))
}

func TestMemoryKVStore_IndexEntryIsRemovedOnDelete(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryKVStore()
	require.NoError(t, s.RegisterEntityIndexes("member", memberIndexes()))
	require.NoError(t, s.Put(ctx, "member/1", memberValue("ana", "alpha")))
	require.NoError(t, s.Delete(ctx, "member/1"))

	keys, err := s.QueryIndex(ctx, "member.Team", []byte("alpha"))
	require.NoError(t, err)
	require.Empty(t, keyStrings(keys),
		"a deleted record must not remain reachable through its index")
}

func TestMemoryKVStore_RegisterBackfillsExistingRecords(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryKVStore()

	// Written BEFORE the index exists.
	require.NoError(t, s.Put(ctx, "member/1", memberValue("ana", "alpha")))
	require.NoError(t, s.RegisterEntityIndexes("member", memberIndexes()))

	keys, err := s.QueryIndex(ctx, "member.Team", []byte("alpha"))
	require.NoError(t, err)
	require.Equal(t, []string{"member/1"}, keyStrings(keys),
		"registration must backfill, or records written before the index are "+
			"invisible to every query that uses it")
}

func TestMemoryKVStore_IndexIgnoresOtherEntityTypes(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryKVStore()
	require.NoError(t, s.RegisterEntityIndexes("member", memberIndexes()))

	require.NoError(t, s.Put(ctx, "member/1", memberValue("ana", "alpha")))
	require.NoError(t, s.Put(ctx, "squad/1", memberValue("zed", "alpha")))

	keys, err := s.QueryIndex(ctx, "member.Team", []byte("alpha"))
	require.NoError(t, err)
	require.Equal(t, []string{"member/1"}, keyStrings(keys),
		"an index registered for one entity type must not collect another's keys")
}

func TestMemoryKVStore_ScanIndexReturnsTheRange(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryKVStore()
	require.NoError(t, s.RegisterEntityIndexes("member", memberIndexes()))
	require.NoError(t, s.Put(ctx, "member/1", memberValue("ana", "alpha")))
	require.NoError(t, s.Put(ctx, "member/2", memberValue("ben", "beta")))
	require.NoError(t, s.Put(ctx, "member/3", memberValue("cal", "gamma")))

	entries, err := s.ScanIndex(ctx, "member.Team", []byte("alpha"), []byte("gamma"))
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"alpha", "beta"}, mapKeys(entries),
		"ScanIndex covers the half-open interval [start, end)")
}

// mapKeys returns the keys of an index scan result.
func mapKeys(m map[string][][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestMemoryKVStore_RegistrationIsIdempotent(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryKVStore()
	require.NoError(t, s.RegisterEntityIndexes("member", memberIndexes()))
	require.NoError(t, s.Put(ctx, "member/1", memberValue("ana", "alpha")))
	require.NoError(t, s.RegisterEntityIndexes("member", memberIndexes()))

	keys, err := s.QueryIndex(ctx, "member.Team", []byte("alpha"))
	require.NoError(t, err)
	require.Equal(t, []string{"member/1"}, keyStrings(keys),
		"re-registering must not drop or duplicate existing entries")
}

// TestMemoryKVStore_UnregisteredIndexRefuses is the negative half of the
// contract: a name nobody registered gets a typed refusal, never an empty
// result that reads as "no matches".
func TestMemoryKVStore_UnregisteredIndexRefuses(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryKVStore()

	_, err := s.QueryIndex(ctx, "member.Team", []byte("alpha"))
	require.ErrorIs(t, err, ErrIndexUnsupported)

	_, err = s.ScanIndex(ctx, "member.Team", []byte("a"), []byte("z"))
	require.ErrorIs(t, err, ErrIndexUnsupported)
}

func TestMemoryKVStore_CompoundIndexesRefuse(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryKVStore()

	err := s.RegisterEntityIndexes("member", []EntityIndex{{
		FieldNames:     []string{"Team", "Name"},
		JSONFieldNames: []string{"team", "name"},
	}})
	require.ErrorIs(t, err, ErrIndexUnsupported,
		"a compound definition this store cannot maintain must be refused at "+
			"registration, not accepted and left inert")

	_, err = s.QueryCompoundIndex(ctx, "member.Team_Name", [][]byte{[]byte("alpha")})
	require.ErrorIs(t, err, ErrIndexUnsupported)

	_, err = s.ScanCompoundIndex(ctx, "member.Team_Name",
		[][]byte{[]byte("alpha")}, nil, nil)
	require.ErrorIs(t, err, ErrIndexUnsupported)
}

// TestMemoryKVStore_UndecodableValueUnderAnIndexIsRefused replaces a test
// that asserted the OPPOSITE — that such writes silently succeed unindexed.
//
// That is fail-open: the write reports success AND successful index
// maintenance, while the record stays invisible to every index query and
// outside every unique constraint, both answering with nil errors. It is
// reachable without doing anything exotic, because a DAO configured with the
// MsgPack or CBOR codec — both re-exported by this change — writes values
// this store cannot decode.
func TestMemoryKVStore_UndecodableValueUnderAnIndexIsRefused(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryKVStore()
	require.NoError(t, s.RegisterEntityIndexes("member", memberIndexes()))

	var unindexable *UnindexableValueError
	err := s.Put(ctx, "member/1", []byte("not json at all"))
	require.ErrorAs(t, err, &unindexable,
		"a value that cannot be decoded must not be stored under an indexed prefix")
	require.Equal(t, "member/1", unindexable.Key)

	got, err := s.Get(ctx, "member/1")
	require.NoError(t, err)
	require.Nil(t, got, "a refused write must leave nothing behind")

	// A binary codec's output is the realistic case.
	require.Error(t, s.Put(ctx, "member/2", []byte{0x82, 0xa4, 0x74, 0x65, 0x61, 0x6d}))
}

// TestMemoryKVStore_UndecodableValueOutsideAnyIndexIsAllowed is the boundary:
// this store holds opaque blobs happily, it just will not pretend to index
// them. Only a prefix an index COVERS is constrained.
func TestMemoryKVStore_UndecodableValueOutsideAnyIndexIsAllowed(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryKVStore()
	require.NoError(t, s.RegisterEntityIndexes("member", memberIndexes()))

	require.NoError(t, s.Put(ctx, "blob/1", []byte("not json at all")),
		"a prefix no index covers is unconstrained")

	got, err := s.Get(ctx, "blob/1")
	require.NoError(t, err)
	require.Equal(t, "not json at all", string(got))
}

// TestMemoryKVStore_DecodableValueMissingTheFieldIsStored keeps the case that
// is genuinely fine: the value decodes, it simply has no value for the
// indexed field, so it is stored and produces no entry. That is sparse-style
// behaviour, not a silent failure.
func TestMemoryKVStore_DecodableValueMissingTheFieldIsStored(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryKVStore()
	require.NoError(t, s.RegisterEntityIndexes("member", memberIndexes()))

	require.NoError(t, s.Put(ctx, "member/1", []byte(`{"name":"ben"}`)))
	require.NoError(t, s.Put(ctx, "member/2", []byte(`{"name":"cal","team":null}`)))

	keys, err := s.QueryIndex(ctx, "member.Team", []byte("alpha"))
	require.NoError(t, err)
	require.Empty(t, keyStrings(keys))
}

func TestMemoryKVStore_NumericFieldsIndexInLiteralForm(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryKVStore()
	require.NoError(t, s.RegisterEntityIndexes("score",
		[]EntityIndex{{FieldName: "Points", JSONFieldName: "points"}}))
	require.NoError(t, s.Put(ctx, "score/1", []byte(`{"points":42}`)))

	keys, err := s.QueryIndex(ctx, "score.Points", []byte("42"))
	require.NoError(t, err)
	require.Equal(t, []string{"score/1"}, keyStrings(keys))
}

// registerNumericScores registers a numeric index on score.points and stores
// one record per value, keyed score/<position>.
func registerNumericScores(t *testing.T, s *MemoryKVStore, values ...string) {
	t.Helper()
	require.NoError(t, s.RegisterEntityIndexes("score",
		[]EntityIndex{{FieldName: "Points", JSONFieldName: "points", Numeric: true}}))
	for i, v := range values {
		key := "score/" + string(rune('a'+i))
		require.NoError(t, s.Put(context.Background(), key, []byte(`{"points":`+v+`}`)))
	}
}

func TestMemoryKVStore_NumericIndexScansInNumericOrder(t *testing.T) {
	s := NewMemoryKVStore()
	registerNumericScores(t, s, "-5", "2", "10", "2.5", "1e21")

	entries, err := s.ScanIndex(context.Background(), "score.Points", []byte("2"), []byte("100"))
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"2", "2.5", "10"}, mapKeys(entries),
		"a numeric scan from 2 includes 10, which text order puts before 2")

	open, err := s.ScanIndex(context.Background(), "score.Points", nil, []byte("0"))
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"-5"}, mapKeys(open))
}

func TestMemoryKVStore_NumericBoundMarkerNamesTheNextValue(t *testing.T) {
	s := NewMemoryKVStore()
	registerNumericScores(t, s, "2", "10", "20")
	ctx := context.Background()

	// A trailing 0x00 moves a lower bound past the number: an exclusive start.
	after, err := s.ScanIndex(ctx, "score.Points", []byte("2\x00"), nil)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"10", "20"}, mapKeys(after))

	// On an upper bound it takes the number in: an inclusive end.
	through, err := s.ScanIndex(ctx, "score.Points", nil, []byte("10\x00"))
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"2", "10"}, mapKeys(through))
}

func TestMemoryKVStore_NumericIndexKeysAreCanonicalDecimals(t *testing.T) {
	s := NewMemoryKVStore()
	registerNumericScores(t, s, "-0", "1.50", "3e2")

	entries, err := s.ScanIndex(context.Background(), "score.Points", nil, nil)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"0", "1.5", "300"}, mapKeys(entries),
		"ScanIndex is keyed by field value; negative zero is zero")

	keys, err := s.QueryIndex(context.Background(), "score.Points", []byte("0"))
	require.NoError(t, err)
	require.Equal(t, []string{"score/a"}, keyStrings(keys))
}

func TestMemoryKVStore_NumericIndexRefusesAnUnparseableBound(t *testing.T) {
	s := NewMemoryKVStore()
	registerNumericScores(t, s, "2")

	for _, bound := range []string{"NaN", "abc", ""} {
		_, err := s.ScanIndex(context.Background(), "score.Points", []byte(bound), nil)
		var invalid *index.InvalidNumericBoundError
		require.ErrorAs(t, err, &invalid, "bound %q", bound)
	}
}

func TestMemoryKVStore_NumericIndexSkipsNonNumbers(t *testing.T) {
	s := NewMemoryKVStore()
	registerNumericScores(t, s, "2", `"ten"`, "true")

	entries, err := s.ScanIndex(context.Background(), "score.Points", nil, nil)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"2"}, mapKeys(entries),
		"a value that is not a number has no place in the numeric order")
}

func TestMemoryKVStore_NumericAndTextDefinitionsConflict(t *testing.T) {
	s := NewMemoryKVStore()
	require.NoError(t, s.RegisterEntityIndexes("score",
		[]EntityIndex{{FieldName: "Points", JSONFieldName: "points"}}))

	err := s.RegisterEntityIndexes("score",
		[]EntityIndex{{FieldName: "Points", JSONFieldName: "points", Numeric: true}})
	var conflict *IndexRegistrationError
	require.ErrorAs(t, err, &conflict,
		"one index name cannot hold both text and numeric terms")
}

func TestMemoryKVStore_ConcurrentWritesAndIndexReadsAreSafe(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryKVStore()
	require.NoError(t, s.RegisterEntityIndexes("member", memberIndexes()))

	const n = 64
	var wg sync.WaitGroup
	wg.Add(n * 2)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			_ = s.Put(ctx, fmt.Sprintf("member/%d", i),
				memberValue(fmt.Sprintf("m%d", i), "alpha"))
		}(i)
		go func() {
			defer wg.Done()
			_, _ = s.QueryIndex(ctx, "member.Team", []byte("alpha"))
		}()
	}
	wg.Wait()

	keys, err := s.QueryIndex(ctx, "member.Team", []byte("alpha"))
	require.NoError(t, err)
	require.Len(t, keyStrings(keys), n,
		"every concurrent write must be reflected in the index exactly once")
}

// TestMemoryKVStore_BackfillRefusesAnUndecodableRecord applies the same rule
// at registration: an existing value the store cannot decode must not be
// silently left out of an index that covers it, because the resulting index
// answers incompletely with a nil error.
func TestMemoryKVStore_BackfillRefusesAnUndecodableRecord(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryKVStore()

	require.NoError(t, s.Put(ctx, "member/1", memberValue("ana", "alpha")))
	require.NoError(t, s.Put(ctx, "member/2", []byte("not json")))

	var unindexable *UnindexableValueError
	err := s.RegisterEntityIndexes("member", memberIndexes())
	require.ErrorAs(t, err, &unindexable)

	// Nothing was published, so the half-built index cannot answer.
	_, qerr := s.QueryIndex(ctx, "member.Team", []byte("alpha"))
	require.ErrorIs(t, qerr, ErrIndexUnsupported)
}

// TestMemoryKVStore_BackfillIndexesOnlyWhatTheFieldCovers keeps the sound
// skips: records that decode but carry no scalar value for the field, and
// records belonging to another entity type.
func TestMemoryKVStore_BackfillIndexesOnlyWhatTheFieldCovers(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryKVStore()

	require.NoError(t, s.Put(ctx, "member/1", memberValue("ana", "alpha")))
	require.NoError(t, s.Put(ctx, "member/3", []byte(`{"name":"cal"}`)))
	require.NoError(t, s.Put(ctx, "member/4", []byte(`{"team":{"nested":"alpha"}}`)))
	require.NoError(t, s.Put(ctx, "squad/1", memberValue("zed", "alpha")))

	require.NoError(t, s.RegisterEntityIndexes("member", memberIndexes()))

	keys, err := s.QueryIndex(ctx, "member.Team", []byte("alpha"))
	require.NoError(t, err)
	require.Equal(t, []string{"member/1"}, keyStrings(keys),
		"backfill must index exactly the records carrying a scalar value for "+
			"the field, and nothing else")
}

// TestMemoryKVStore_DeleteOfARecordWithNoIndexEntrySucceeds covers removal of
// records that produced no index entry — a decodable value missing the field,
// and a record under another entity type's prefix. Removal must not fail
// looking for an entry that was never written.
func TestMemoryKVStore_DeleteOfARecordWithNoIndexEntrySucceeds(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryKVStore()
	require.NoError(t, s.RegisterEntityIndexes("member", memberIndexes()))

	require.NoError(t, s.Put(ctx, "member/2", []byte(`{"name":"ben"}`)))
	require.NoError(t, s.Put(ctx, "squad/1", memberValue("zed", "alpha")))

	require.NoError(t, s.Delete(ctx, "member/2"))
	require.NoError(t, s.Delete(ctx, "squad/1"))
}

// TestMemoryKVStore_NonScalarFieldsAreNotIndexed pins that an object or array
// under an indexed field produces no entry rather than an index key derived
// from its JSON text, which would match nothing a caller could ever query for.
func TestMemoryKVStore_NonScalarFieldsAreNotIndexed(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryKVStore()
	require.NoError(t, s.RegisterEntityIndexes("member", memberIndexes()))

	require.NoError(t, s.Put(ctx, "member/1", []byte(`{"team":{"name":"alpha"}}`)))
	require.NoError(t, s.Put(ctx, "member/2", []byte(`{"team":["alpha"]}`)))

	for _, probe := range []string{`{"name":"alpha"}`, `["alpha"]`, "alpha"} {
		keys, err := s.QueryIndex(ctx, "member.Team", []byte(probe))
		require.NoError(t, err)
		require.Empty(t, keyStrings(keys), "probe %q must match nothing", probe)
	}
}

func TestMemoryKVStore_BooleanFieldsIndexInLiteralForm(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryKVStore()
	require.NoError(t, s.RegisterEntityIndexes("flag",
		[]EntityIndex{{FieldName: "Active", JSONFieldName: "active"}}))
	require.NoError(t, s.Put(ctx, "flag/1", []byte(`{"active":true}`)))

	keys, err := s.QueryIndex(ctx, "flag.Active", []byte("true"))
	require.NoError(t, err)
	require.Equal(t, []string{"flag/1"}, keyStrings(keys))
}

// TestMemoryKVStore_UniqueIndexRejectsADuplicate pins that the Unique flag
// reaches the underlying index and is enforced, rather than being accepted and
// ignored — an unenforced unique constraint is a lie the caller relies on.
func TestMemoryKVStore_UniqueIndexRejectsADuplicate(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryKVStore()
	require.NoError(t, s.RegisterEntityIndexes("user",
		[]EntityIndex{{FieldName: "Email", JSONFieldName: "email", Unique: true}}))

	require.NoError(t, s.Put(ctx, "user/1", []byte(`{"email":"a@example.com"}`)))
	err := s.Put(ctx, "user/2", []byte(`{"email":"a@example.com"}`))
	require.Error(t, err, "a second entity claiming a unique value must be refused")
}

func TestMemoryKVStore_UniqueIndexAllowsUpdatingTheSameEntity(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryKVStore()
	require.NoError(t, s.RegisterEntityIndexes("user",
		[]EntityIndex{{FieldName: "Email", JSONFieldName: "email", Unique: true}}))
	require.NoError(t, s.Put(ctx, "user/1", []byte(`{"email":"a@example.com","name":"ana"}`)))

	require.NoError(t, s.Put(ctx, "user/1", []byte(`{"email":"a@example.com","name":"anna"}`)),
		"an entity re-writing its own unique value is not a violation")
}

func TestMemoryKVStore_UniqueViolationLeavesTheStoreUntouched(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryKVStore()
	require.NoError(t, s.RegisterEntityIndexes("user",
		[]EntityIndex{{FieldName: "Email", JSONFieldName: "email", Unique: true}}))
	require.NoError(t, s.Put(ctx, "user/1", []byte(`{"email":"a@example.com"}`)))

	var violation *index.UniqueViolationError
	err := s.Put(ctx, "user/2", []byte(`{"email":"a@example.com"}`))
	require.ErrorAs(t, err, &violation, "the refusal must be the typed index error")
	require.Equal(t, "user.Email", violation.IndexName)

	// Neither the data nor the index may carry the rejected write.
	got, err := s.Get(ctx, "user/2")
	require.NoError(t, err)
	require.Nil(t, got, "a rejected write must leave no value behind")

	keys, err := s.QueryIndex(ctx, "user.Email", []byte("a@example.com"))
	require.NoError(t, err)
	require.Equal(t, []string{"user/1"}, keyStrings(keys),
		"a rejected write must leave no index entry behind")
}

func TestMemoryKVStore_UniqueValueIsReusableAfterDelete(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryKVStore()
	require.NoError(t, s.RegisterEntityIndexes("user",
		[]EntityIndex{{FieldName: "Email", JSONFieldName: "email", Unique: true}}))
	require.NoError(t, s.Put(ctx, "user/1", []byte(`{"email":"a@example.com"}`)))
	require.NoError(t, s.Delete(ctx, "user/1"))

	require.NoError(t, s.Put(ctx, "user/2", []byte(`{"email":"a@example.com"}`)),
		"once the holder is gone the value is free again")
}

func TestMemoryKVStore_PutIfAbsentRefusesAUniqueViolation(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryKVStore()
	require.NoError(t, s.RegisterEntityIndexes("user",
		[]EntityIndex{{FieldName: "Email", JSONFieldName: "email", Unique: true}}))
	require.NoError(t, s.Put(ctx, "user/1", []byte(`{"email":"a@example.com"}`)))

	stored, err := s.PutIfAbsent(ctx, "user/2", []byte(`{"email":"a@example.com"}`))
	require.Error(t, err, "an absent key does not excuse a unique violation")
	require.False(t, stored)
}

func TestMemoryKVStore_NonUniqueIndexAcceptsDuplicates(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryKVStore()
	require.NoError(t, s.RegisterEntityIndexes("member", memberIndexes()))

	require.NoError(t, s.Put(ctx, "member/1", memberValue("ana", "alpha")))
	require.NoError(t, s.Put(ctx, "member/2", memberValue("ben", "alpha")),
		"the uniqueness check must apply only to indexes declared unique")
}

// TestMemoryKVStore_FailedBackfillPublishesNoIndex covers a review finding: the
// index used to be published into the store BEFORE it was backfilled, so a
// backfill that failed — a unique definition over data that already holds
// duplicates is the obvious case — left a partially populated index visible.
// Every later registration then skipped it as "already exists", and queries
// answered incompletely with a nil error: the silent-empty class again, one
// level down.
func TestMemoryKVStore_FailedBackfillPublishesNoIndex(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryKVStore()

	// Two records already sharing a value the new index declares unique.
	require.NoError(t, s.Put(ctx, "user/1", []byte(`{"email":"a@example.com"}`)))
	require.NoError(t, s.Put(ctx, "user/2", []byte(`{"email":"a@example.com"}`)))

	err := s.RegisterEntityIndexes("user",
		[]EntityIndex{{FieldName: "Email", JSONFieldName: "email", Unique: true}})
	require.Error(t, err, "a unique index over existing duplicates must fail registration")

	var violation *index.UniqueViolationError
	require.ErrorAs(t, err, &violation)

	// Nothing was published: the index must not answer at all, rather than
	// answer from a half-filled map.
	_, qerr := s.QueryIndex(ctx, "user.Email", []byte("a@example.com"))
	require.ErrorIs(t, qerr, ErrIndexUnsupported,
		"a failed registration must leave no queryable index behind")
}

// TestMemoryKVStore_FailedBackfillCanBeRetried follows from the above: because
// nothing was published, fixing the data and registering again must work. If a
// partial index had been left in place the retry would silently no-op.
func TestMemoryKVStore_FailedBackfillCanBeRetried(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryKVStore()
	require.NoError(t, s.Put(ctx, "user/1", []byte(`{"email":"a@example.com"}`)))
	require.NoError(t, s.Put(ctx, "user/2", []byte(`{"email":"a@example.com"}`)))

	defs := []EntityIndex{{FieldName: "Email", JSONFieldName: "email", Unique: true}}
	require.Error(t, s.RegisterEntityIndexes("user", defs))

	require.NoError(t, s.Delete(ctx, "user/2"))
	require.NoError(t, s.RegisterEntityIndexes("user", defs),
		"once the duplicate is gone the registration must succeed")

	keys, err := s.QueryIndex(ctx, "user.Email", []byte("a@example.com"))
	require.NoError(t, err)
	require.Equal(t, []string{"user/1"}, keyStrings(keys))
}

// TestMemoryKVStore_RegisterHonoursAnExplicitIndexName pins the resolved-name
// contract. A caller that resolves index names differently — a DAO configured
// with WithDatabase — supplies IndexName, and the store must create the index
// under exactly that name rather than composing its own.
func TestMemoryKVStore_RegisterHonoursAnExplicitIndexName(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryKVStore()

	require.NoError(t, s.RegisterEntityIndexes("member", []EntityIndex{{
		IndexName:     "tenantdb.member.Team",
		FieldName:     "Team",
		JSONFieldName: "team",
	}}))
	require.NoError(t, s.Put(ctx, "member/1", memberValue("ana", "alpha")))

	keys, err := s.QueryIndex(ctx, "tenantdb.member.Team", []byte("alpha"))
	require.NoError(t, err)
	require.Equal(t, []string{"member/1"}, keyStrings(keys))

	// The unqualified name must NOT exist, or the explicit name was ignored
	// and a second index was created alongside it.
	_, err = s.QueryIndex(ctx, "member.Team", []byte("alpha"))
	require.ErrorIs(t, err, ErrIndexUnsupported,
		"supplying IndexName must not also create the composed name")
}

func TestMemoryKVStore_RegistrationsAccumulateAcrossCalls(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryKVStore()

	require.NoError(t, s.RegisterEntityIndexes("member", memberIndexes()))
	require.NoError(t, s.RegisterEntityIndexes("member",
		[]EntityIndex{{FieldName: "Name", JSONFieldName: "name"}}))
	require.NoError(t, s.Put(ctx, "member/1", memberValue("ana", "alpha")))

	// Both indexes answer: the second call must not have replaced the first.
	byTeam, err := s.QueryIndex(ctx, "member.Team", []byte("alpha"))
	require.NoError(t, err)
	require.Equal(t, []string{"member/1"}, keyStrings(byTeam))

	byName, err := s.QueryIndex(ctx, "member.Name", []byte("ana"))
	require.NoError(t, err)
	require.Equal(t, []string{"member/1"}, keyStrings(byName))
}

// TestMemoryKVStore_CaseInsensitiveIndexMatchesEitherCasing covers a review
// finding. The store accepted CaseSensitive:false into the index config and
// then never normalized anything, so a lookup for "Alpha" answered EMPTY over
// a record stored as "alpha" — with a nil error, which is the silent-empty
// class this store exists to be trustworthy about. `index:"case_insensitive"`
// is a supported tag, so this was reachable from an ordinary entity.
func TestMemoryKVStore_CaseInsensitiveIndexMatchesEitherCasing(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryKVStore()
	require.NoError(t, s.RegisterEntityIndexes("member", []EntityIndex{
		{FieldName: "Team", JSONFieldName: "team", CaseSensitive: false},
	}))
	require.NoError(t, s.Put(ctx, "member/1", memberValue("ana", "Alpha")))

	for _, probe := range []string{"Alpha", "alpha", "ALPHA"} {
		keys, err := s.QueryIndex(ctx, "member.Team", []byte(probe))
		require.NoError(t, err)
		require.Equalf(t, []string{"member/1"}, keyStrings(keys),
			"a case-insensitive index must match probe %q", probe)
	}
}

func TestMemoryKVStore_CaseSensitiveIndexDistinguishesCasing(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryKVStore()
	require.NoError(t, s.RegisterEntityIndexes("member", []EntityIndex{
		{FieldName: "Team", JSONFieldName: "team", CaseSensitive: true},
	}))
	require.NoError(t, s.Put(ctx, "member/1", memberValue("ana", "Alpha")))

	keys, err := s.QueryIndex(ctx, "member.Team", []byte("Alpha"))
	require.NoError(t, err)
	require.Equal(t, []string{"member/1"}, keyStrings(keys))

	keys, err = s.QueryIndex(ctx, "member.Team", []byte("alpha"))
	require.NoError(t, err)
	require.Empty(t, keyStrings(keys),
		"a case-SENSITIVE index must not match a differently cased probe")
}

// TestMemoryKVStore_CaseInsensitiveUniqueRejectsDifferentCasing pins the other
// half of the same finding: without normalization a unique case-insensitive
// index would admit both "A" and "a", accepting a duplicate the constraint
// forbids.
func TestMemoryKVStore_CaseInsensitiveUniqueRejectsDifferentCasing(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryKVStore()
	require.NoError(t, s.RegisterEntityIndexes("user", []EntityIndex{
		{FieldName: "Email", JSONFieldName: "email", Unique: true, CaseSensitive: false},
	}))
	require.NoError(t, s.Put(ctx, "user/1", []byte(`{"email":"A@Example.com"}`)))

	err := s.Put(ctx, "user/2", []byte(`{"email":"a@example.com"}`))
	require.Error(t, err,
		"a case-insensitive unique index must treat differently cased values as the same")
	var violation *index.UniqueViolationError
	require.ErrorAs(t, err, &violation)
}

func TestMemoryKVStore_CaseInsensitiveIndexIsBackfilledNormalized(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryKVStore()
	require.NoError(t, s.Put(ctx, "member/1", memberValue("ana", "Alpha")))
	require.NoError(t, s.RegisterEntityIndexes("member", []EntityIndex{
		{FieldName: "Team", JSONFieldName: "team", CaseSensitive: false},
	}))

	keys, err := s.QueryIndex(ctx, "member.Team", []byte("alpha"))
	require.NoError(t, err)
	require.Equal(t, []string{"member/1"}, keyStrings(keys),
		"backfill must normalize too, or records written before the index are "+
			"reachable only by their exact original casing")
}

func TestMemoryKVStore_CaseInsensitiveIndexIsCleanedUpNormalized(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryKVStore()
	require.NoError(t, s.RegisterEntityIndexes("member", []EntityIndex{
		{FieldName: "Team", JSONFieldName: "team", CaseSensitive: false},
	}))
	require.NoError(t, s.Put(ctx, "member/1", memberValue("ana", "Alpha")))
	require.NoError(t, s.Delete(ctx, "member/1"))

	keys, err := s.QueryIndex(ctx, "member.Team", []byte("alpha"))
	require.NoError(t, err)
	require.Empty(t, keyStrings(keys),
		"removal must use the same normalized term the write used, or the entry "+
			"outlives the record")
}

// TestMemoryKVStore_SparseIndexSkipsEmptyValues covers the review's advisory:
// Sparse was accepted and ignored. A sparse index must omit entities whose
// field is empty rather than indexing them under "", which would make
// "missing" and "empty" the same query.
func TestMemoryKVStore_SparseIndexSkipsEmptyValues(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryKVStore()
	require.NoError(t, s.RegisterEntityIndexes("member", []EntityIndex{
		{FieldName: "Team", JSONFieldName: "team", Sparse: true},
	}))
	require.NoError(t, s.Put(ctx, "member/1", memberValue("ana", "")))
	require.NoError(t, s.Put(ctx, "member/2", memberValue("ben", "alpha")))

	empty, err := s.QueryIndex(ctx, "member.Team", []byte(""))
	require.NoError(t, err)
	require.Empty(t, keyStrings(empty),
		"a sparse index must not index the empty value")

	present, err := s.QueryIndex(ctx, "member.Team", []byte("alpha"))
	require.NoError(t, err)
	require.Equal(t, []string{"member/2"}, keyStrings(present))
}

func TestMemoryKVStore_NonSparseIndexKeepsEmptyValues(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryKVStore()
	require.NoError(t, s.RegisterEntityIndexes("member", memberIndexes()))
	require.NoError(t, s.Put(ctx, "member/1", memberValue("ana", "")))

	keys, err := s.QueryIndex(ctx, "member.Team", []byte(""))
	require.NoError(t, err)
	require.Equal(t, []string{"member/1"}, keyStrings(keys),
		"without Sparse the empty value is a value like any other")
}

// TestMemoryKVStore_SparseScanWithExplicitEmptyUpperBoundMatchesNothing covers
// a review finding. ScanIndex normalized its bounds with term(), the helper
// built for VALUES, and discarded its ok flag. On a sparse index
// term([]byte("")) returns (nil, false) — sparse omits empty values — and the
// underlying index reads a nil bound as UNBOUNDED. So the empty half-open
// range [min, "") returned every entry in the index instead of none: the
// widest possible wrong answer, from a bound that asked for nothing.
//
// A bound is not a value. Sparse governs which values get indexed, not how a
// bound is read, and an explicit empty bound must stay empty.
func TestMemoryKVStore_SparseScanWithExplicitEmptyUpperBoundMatchesNothing(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryKVStore()
	require.NoError(t, s.RegisterEntityIndexes("member", []EntityIndex{
		{FieldName: "Team", JSONFieldName: "team", Sparse: true},
	}))
	require.NoError(t, s.Put(ctx, "member/1", memberValue("ana", "alpha")))
	require.NoError(t, s.Put(ctx, "member/2", memberValue("ben", "beta")))

	entries, err := s.ScanIndex(ctx, "member.Team", nil, []byte(""))
	require.NoError(t, err)
	require.Empty(t, mapKeys(entries),
		"[min, \"\") is an EMPTY range; an explicit empty upper bound must not "+
			"become an open one")
}

func TestMemoryKVStore_ScanWithNilBoundsIsUnbounded(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryKVStore()
	require.NoError(t, s.RegisterEntityIndexes("member", memberIndexes()))
	require.NoError(t, s.Put(ctx, "member/1", memberValue("ana", "alpha")))
	require.NoError(t, s.Put(ctx, "member/2", memberValue("ben", "beta")))

	entries, err := s.ScanIndex(ctx, "member.Team", nil, nil)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"alpha", "beta"}, mapKeys(entries),
		"a nil bound means open, and must stay nil")
}

func TestMemoryKVStore_ScanBoundsAreCaseNormalized(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryKVStore()
	require.NoError(t, s.RegisterEntityIndexes("member", []EntityIndex{
		{FieldName: "Team", JSONFieldName: "team", CaseSensitive: false},
	}))
	require.NoError(t, s.Put(ctx, "member/1", memberValue("ana", "Alpha")))
	require.NoError(t, s.Put(ctx, "member/2", memberValue("ben", "Delta")))

	// Upper-case bounds sort BEFORE lower-case terms in raw byte order, so
	// without normalization this range would come back empty.
	entries, err := s.ScanIndex(ctx, "member.Team", []byte("A"), []byte("C"))
	require.NoError(t, err)
	require.Equal(t, []string{"alpha"}, mapKeys(entries),
		"bounds must be normalized into the same space as the stored terms")
}

// TestMemoryKVStore_ConflictingRegistrationIsRefused covers the review's
// advisory. Registration is idempotent for an IDENTICAL definition, but
// silently keeping the first when the second differs hands the second caller
// an index whose sparse, unique or case-sensitivity behaviour is not what it
// asked for — discovered as wrong query results rather than as an error.
func TestMemoryKVStore_ConflictingRegistrationIsRefused(t *testing.T) {
	s := NewMemoryKVStore()
	base := []EntityIndex{{FieldName: "Team", JSONFieldName: "team"}}
	require.NoError(t, s.RegisterEntityIndexes("member", base))

	conflicts := map[string][]EntityIndex{
		"sparse": {{FieldName: "Team", JSONFieldName: "team", Sparse: true}},
		"unique": {{FieldName: "Team", JSONFieldName: "team", Unique: true}},
		"case":   {{FieldName: "Team", JSONFieldName: "team", CaseSensitive: true}},
		"field":  {{FieldName: "Team", JSONFieldName: "squad"}},
	}
	for name, defs := range conflicts {
		t.Run(name, func(t *testing.T) {
			err := s.RegisterEntityIndexes("member", defs)
			var conflict *ConflictingIndexError
			require.ErrorAs(t, err, &conflict,
				"a conflicting %s definition must be refused, not silently ignored", name)
			require.Equal(t, "member.Team", conflict.IndexName)
		})
	}

	// The identical definition is still a no-op.
	require.NoError(t, s.RegisterEntityIndexes("member", base))
}

// TestMemoryKVStore_IndexReadNeverObservesAHalfAppliedWrite covers a review
// finding: QueryIndex used to release the store lock as soon as it had the
// index pointer.
//
// A Put updates an index in several steps under the write lock — remove the
// previous value's term, replace the data, add the new term. The index's own
// mutex prevents a data race but does not make those steps one operation, so
// a reader holding only the pointer could run between them and find the key
// under NEITHER term, reporting "no matches" with a nil error. That is the
// silent-empty failure arriving through concurrency instead of through a
// missing capability.
//
// TestMemoryKVStore_IndexReadNeverObservesAHalfAppliedWrite covers a review
// finding: QueryIndex used to release the store lock as soon as it had the
// index pointer.
//
// A Put updates an index in several steps under the write lock — remove the
// previous value's term, replace the data, add the new term. The index's own
// mutex prevents a data race but does not make those steps one operation, so
// a reader holding only the pointer could run between them and find the key
// under NEITHER term, reporting "no matches" with a nil error. That is the
// silent-empty failure arriving through concurrency instead of through a
// missing capability.
//
// The writer here changes only the UNINDEXED field, so the indexed Team never
// changes value — every Put still removes and re-adds the same term, which is
// exactly the window the bug opened. A correct store therefore answers
// "member/1" on every single read; there is no legitimate interleaving in
// which the member is absent.
func TestMemoryKVStore_IndexReadNeverObservesAHalfAppliedWrite(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryKVStore()
	require.NoError(t, s.RegisterEntityIndexes("member", memberIndexes()))
	require.NoError(t, s.Put(ctx, "member/1", memberValue("ana", "alpha")))

	const rounds = 5000
	done := make(chan struct{})
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(done)
		for i := 0; i < rounds; i++ {
			if err := s.Put(ctx, "member/1",
				memberValue(fmt.Sprintf("ana-%d", i), "alpha")); err != nil {
				return
			}
		}
	}()

	var missing atomic.Int64
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
			}
			keys, err := s.QueryIndex(ctx, "member.Team", []byte("alpha"))
			if err != nil {
				continue
			}
			if len(keys) == 0 {
				missing.Add(1)
			}
		}
	}()

	wg.Wait()
	require.Zero(t, missing.Load(),
		"an index read observed a Put's index update half-applied: the member's "+
			"team never changed, so it must be found on every read")
}

// TestMemoryKVStore_ScanIndexNeverObservesAHalfAppliedWrite is the same
// property for the range path, which had the identical lock shape.
func TestMemoryKVStore_ScanIndexNeverObservesAHalfAppliedWrite(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryKVStore()
	require.NoError(t, s.RegisterEntityIndexes("member", memberIndexes()))
	require.NoError(t, s.Put(ctx, "member/1", memberValue("ana", "alpha")))

	const rounds = 4000
	done := make(chan struct{})
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(done)
		for i := 0; i < rounds; i++ {
			team := "alpha"
			if i%2 == 1 {
				team = "beta"
			}
			if err := s.Put(ctx, "member/1", memberValue("ana", team)); err != nil {
				return
			}
		}
	}()

	var torn atomic.Int64
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
			}
			// One scan covering both terms: the member must appear under
			// exactly one of them.
			entries, err := s.ScanIndex(ctx, "member.Team", []byte("alpha"), []byte("c"))
			if err != nil {
				continue
			}
			if len(entries) == 0 {
				torn.Add(1)
			}
		}
	}()

	wg.Wait()
	require.Zero(t, torn.Load(),
		"a range scan observed a Put's index update half-applied and returned "+
			"an empty range over data that always holds a match")
}

// TestMemoryKVStore_TopLevelJSONNullIsRefused covers a review finding.
//
// The document `null` unmarshals into a map[string]json.RawMessage WITHOUT
// error and leaves it nil, so it passed the indexability check as a decodable
// object and then produced no index entries: the record existed under an
// indexed prefix and was invisible to every index query, with no error
// anywhere. That is exactly the fail-open state this store is built to make
// impossible, reached through the one JSON document that decodes to nothing.
func TestMemoryKVStore_TopLevelJSONNullIsRefused(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryKVStore()
	require.NoError(t, s.RegisterEntityIndexes("member", memberIndexes()))

	var unindexable *UnindexableValueError
	require.ErrorAs(t, s.Put(ctx, "member/1", []byte("null")), &unindexable)
	require.Equal(t, "member/1", unindexable.Key)

	got, err := s.Get(ctx, "member/1")
	require.NoError(t, err)
	require.Nil(t, got, "a refused write must leave nothing behind")
}

// TestMemoryKVStore_BackfillRefusesTopLevelJSONNull is the same value on the
// registration path, which shares the decoder.
func TestMemoryKVStore_BackfillRefusesTopLevelJSONNull(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryKVStore()
	require.NoError(t, s.Put(ctx, "member/1", []byte("null")))

	var unindexable *UnindexableValueError
	require.ErrorAs(t, s.RegisterEntityIndexes("member", memberIndexes()), &unindexable)

	_, qerr := s.QueryIndex(ctx, "member.Team", []byte("alpha"))
	require.ErrorIs(t, qerr, ErrIndexUnsupported,
		"a failed registration must publish no index")
}

// TestMemoryKVStore_EmptyJSONObjectIsAccepted is the boundary the fix must not
// cross. `{}` decodes to a non-nil EMPTY map — a real object that simply
// carries no value for the indexed field, which is the ordinary sparse case
// and must still be storable.
func TestMemoryKVStore_EmptyJSONObjectIsAccepted(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryKVStore()
	require.NoError(t, s.RegisterEntityIndexes("member", memberIndexes()))

	require.NoError(t, s.Put(ctx, "member/1", []byte(`{}`)),
		"an empty object is a decodable object, not an undecodable value")

	got, err := s.Get(ctx, "member/1")
	require.NoError(t, err)
	require.JSONEq(t, `{}`, string(got))

	keys, err := s.QueryIndex(ctx, "member.Team", []byte("alpha"))
	require.NoError(t, err)
	require.Empty(t, keyStrings(keys))
}

// A guarded delete is a DELETE: it must retract index entries exactly as
// Delete does. Removing only the primary value leaves QueryIndex returning a
// key that is gone, and leaves a stale UNIQUE entry rejecting a later
// legitimate insert of the same value. The guard changes WHEN a delete
// happens, never what a delete means.
func TestMemoryKVStore_CompareAndDelete_RetractsIndexes(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryKVStore()

	if err := s.RegisterEntityIndexes("person", []EntityIndex{
		{FieldName: "Email", JSONFieldName: "email"},
	}); err != nil {
		t.Fatalf("RegisterEntityIndexes: %v", err)
	}

	const key = "person/1"
	record := []byte(`{"email":"a@example.com"}`)
	if err := s.Put(ctx, key, record); err != nil {
		t.Fatalf("Put: %v", err)
	}
	ids, err := s.QueryIndex(ctx, "person.Email", []byte("a@example.com"))
	if err != nil || len(ids) != 1 {
		t.Fatalf("precondition: QueryIndex = (%v, %v), want one hit", ids, err)
	}

	// A REFUSED guard changes nothing, index included.
	deleted, err := s.CompareAndDelete(ctx, key, []byte(`{"email":"other@example.com"}`))
	if err != nil || deleted {
		t.Fatalf("refused guard = (%v, %v), want (false, nil)", deleted, err)
	}
	if ids, qerr := s.QueryIndex(ctx, "person.Email", []byte("a@example.com")); qerr != nil || len(ids) != 1 {
		t.Fatalf("index after a refused guard = (%v, %v), want it intact", ids, qerr)
	}

	// A successful guarded delete retracts the entry.
	deleted, err = s.CompareAndDelete(ctx, key, record)
	if err != nil || !deleted {
		t.Fatalf("CompareAndDelete = (%v, %v), want (true, nil)", deleted, err)
	}
	ids, err = s.QueryIndex(ctx, "person.Email", []byte("a@example.com"))
	if err != nil {
		t.Fatalf("QueryIndex: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("index still returns %d id(s) for a deleted key — a guarded "+
			"delete left the index stale", len(ids))
	}
}

// A stale UNIQUE index entry is the sharper consequence: it rejects a later,
// legitimate insert of the same value.
func TestMemoryKVStore_CompareAndDelete_FreesAUniqueIndexValue(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryKVStore()

	if err := s.RegisterEntityIndexes("person", []EntityIndex{
		{FieldName: "Email", JSONFieldName: "email", Unique: true},
	}); err != nil {
		t.Fatalf("RegisterEntityIndexes: %v", err)
	}

	record := []byte(`{"email":"a@example.com"}`)
	if err := s.Put(ctx, "person/1", record); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if deleted, err := s.CompareAndDelete(ctx, "person/1", record); err != nil || !deleted {
		t.Fatalf("CompareAndDelete = (%v, %v), want (true, nil)", deleted, err)
	}

	// The value is free again. A stale unique entry would refuse this.
	if err := s.Put(ctx, "person/2", record); err != nil {
		t.Fatalf("re-inserting the freed unique value: %v — the guarded delete "+
			"left a stale unique index entry", err)
	}
}

// The guarded swap decides on the stored value, and reports the two outcomes
// apart: swapped=true with the new value stored, or swapped=false with the
// store untouched.
func TestMemoryKVStore_CompareAndSwap(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name        string
		seed        *string // nil means the key is absent
		expected    string
		next        string
		wantSwapped bool
		wantStored  *string // nil means still absent
	}{
		{
			name: "matching value swaps", seed: ptr("old"), expected: "old", next: "new",
			wantSwapped: true, wantStored: ptr("new"),
		},
		{
			name: "different value is refused", seed: ptr("old"), expected: "stale", next: "new",
			wantSwapped: false, wantStored: ptr("old"),
		},
		{
			// The distinction from PutIfAbsent, and the one most likely to be
			// got wrong: a swap must NOT create.
			name: "absent key is not created", seed: nil, expected: "", next: "new",
			wantSwapped: false, wantStored: nil,
		},
		{
			// An empty stored value is a real value, not absence, and it is
			// guardable.
			name: "empty stored value is swappable", seed: ptr(""), expected: "", next: "new",
			wantSwapped: true, wantStored: ptr("new"),
		},
		{
			name: "swapping to an empty value is a swap, not a delete", seed: ptr("old"), expected: "old", next: "",
			wantSwapped: true, wantStored: ptr(""),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewMemoryKVStore()
			const key = "k"
			if tt.seed != nil {
				if err := s.Put(ctx, key, []byte(*tt.seed)); err != nil {
					t.Fatalf("Put: %v", err)
				}
			}

			swapped, err := s.CompareAndSwap(ctx, key, []byte(tt.expected), []byte(tt.next))
			if err != nil {
				t.Fatalf("CompareAndSwap: %v", err)
			}
			if swapped != tt.wantSwapped {
				t.Errorf("swapped = %v, want %v", swapped, tt.wantSwapped)
			}

			got, gerr := s.Get(ctx, key)
			if gerr != nil {
				t.Fatalf("Get: %v", gerr)
			}
			switch {
			case tt.wantStored == nil:
				if got != nil {
					t.Errorf("stored = %q, want the key still absent", got)
				}
			case got == nil:
				t.Errorf("key is absent, want %q stored", *tt.wantStored)
			case string(got) != *tt.wantStored:
				t.Errorf("stored = %q, want %q", got, *tt.wantStored)
			}
		})
	}
}

func ptr(s string) *string { return &s }

// A guarded swap REPLACES the index entries, and a refused one changes
// nothing.
//
// This is the half most easily got wrong: retracting the old entry without
// adding the new one leaves the record invisible to index queries, and adding
// without retracting leaves the key answering to a value it no longer holds.
// The guard changes when the write happens, never what a write means.
func TestMemoryKVStore_CompareAndSwap_ReplacesIndexEntries(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryKVStore()

	if err := s.RegisterEntityIndexes("person", []EntityIndex{
		{FieldName: "Email", JSONFieldName: "email"},
	}); err != nil {
		t.Fatalf("RegisterEntityIndexes: %v", err)
	}

	const key = "person/1"
	before := []byte(`{"email":"a@example.com"}`)
	after := []byte(`{"email":"b@example.com"}`)
	if err := s.Put(ctx, key, before); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// A refused guard leaves the index exactly as it was.
	swapped, err := s.CompareAndSwap(ctx, key, []byte(`{"email":"wrong@example.com"}`), after)
	if err != nil || swapped {
		t.Fatalf("refused guard = (%v, %v), want (false, nil)", swapped, err)
	}
	if ids, qerr := s.QueryIndex(ctx, "person.Email", []byte("a@example.com")); qerr != nil || len(ids) != 1 {
		t.Fatalf("index after a refused guard = (%v, %v), want it intact", ids, qerr)
	}

	swapped, err = s.CompareAndSwap(ctx, key, before, after)
	if err != nil || !swapped {
		t.Fatalf("CompareAndSwap = (%v, %v), want (true, nil)", swapped, err)
	}

	old, err := s.QueryIndex(ctx, "person.Email", []byte("a@example.com"))
	if err != nil {
		t.Fatalf("QueryIndex(old): %v", err)
	}
	if len(old) != 0 {
		t.Errorf("the OLD index value still returns %d id(s); the swap did not retract it", len(old))
	}

	fresh, err := s.QueryIndex(ctx, "person.Email", []byte("b@example.com"))
	if err != nil {
		t.Fatalf("QueryIndex(new): %v", err)
	}
	if len(fresh) != 1 {
		t.Errorf("the NEW index value returns %d id(s), want 1; the swap did not index it", len(fresh))
	}
}

// Concurrent swappers from the same expected value: exactly one wins.
//
// This is the property the whole opcode exists for. A read-then-write under a
// caller-side mutex passes every single-threaded test above and fails this one
// the moment there are two writers.
func TestMemoryKVStore_CompareAndSwap_AdmitsExactlyOneOfNContenders(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryKVStore()
	const key = "k"
	if err := s.Put(ctx, key, []byte("start")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	const contenders = 24
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		winners []string
	)
	start := make(chan struct{})
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			mine := "winner-" + strconv.Itoa(id)
			<-start // release them together, so the swaps really contend
			swapped, err := s.CompareAndSwap(ctx, key, []byte("start"), []byte(mine))
			if err != nil {
				t.Errorf("contender %d: %v", id, err)
				return
			}
			if swapped {
				mu.Lock()
				winners = append(winners, mine)
				mu.Unlock()
			}
		}(i)
	}
	close(start)
	wg.Wait()

	if len(winners) != 1 {
		t.Fatalf("%d contenders swapped from the same expected value, want exactly 1: %v",
			len(winners), winners)
	}
	stored, err := s.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(stored) != winners[0] {
		t.Errorf("stored = %q, want the winner's value %q", stored, winners[0])
	}
}
