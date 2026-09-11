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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
)

// ListAll is the unfiltered peer of List, and the ONE property worth testing
// on each engine is that the two disagree in exactly one way: ListAll returns
// the internal keys the discovery guard removes from List, and returns
// nothing else that List does not.
//
// It is tested per engine rather than once, because each implements List
// separately -- a prefix query, a range scan, and a backend passthrough -- and
// an engine whose ListAll accidentally filtered would make a database drop a
// silent no-op on that deployment shape alone (go-qrdb#412).

// tsKey is an internal key the guard removes: it begins with a reserved
// prefix, and that prefix is not one whose owner enumerates its own records,
// so nothing waives the filter for it.
const tsKey = timeseries.KeyPrefixPoints + "acme/reports/cpu:0001"

// userKey is an ordinary key the guard admits.
const userKey = "acme/reports/greeting"

// TestKVService_ListAll_ReturnsWhatListFilters covers the Raft-backed engine.
func TestKVService_ListAll_ReturnsWhatListFilters(t *testing.T) {
	ctx := context.Background()
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return []string{tsKey, userKey}, nil
		},
	}
	svc := NewKVService(host, 1)

	filtered, err := svc.List(ctx, "")
	require.NoError(t, err)
	assert.Equal(t, []string{userKey}, filtered,
		"List is the user-facing enumeration and must hide the internal key")

	all, err := svc.ListAll(ctx, "")
	require.NoError(t, err)
	assert.Equal(t, []string{tsKey, userKey}, all,
		"ListAll must return the internal key a teardown has to delete")
}

// TestKVService_ListAll_PropagatesAReadFailure is the error path: a
// reclamation must fail loudly rather than read an empty list as "nothing to
// do".
func TestKVService_ListAll_PropagatesAReadFailure(t *testing.T) {
	boom := errors.New("read failed")
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, boom
		},
	}

	_, err := NewKVService(host, 1).ListAll(context.Background(), "")
	require.Error(t, err)
}

// TestKVService_ListAll_RefusesAnUnexpectedResult covers the type guard, which
// must not degrade to an empty list for the same reason.
func TestKVService_ListAll_RefusesAnUnexpectedResult(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return 42, nil
		},
	}

	_, err := NewKVService(host, 1).ListAll(context.Background(), "")
	var want *ReadError
	require.ErrorAs(t, err, &want)
}

// TestKVService_ListAll_NilResultIsEmpty pins the third branch: a shard with
// nothing under the prefix is success with no keys, which is what lets a
// re-issued drop finish an interrupted one.
func TestKVService_ListAll_NilResultIsEmpty(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, nil
		},
	}

	keys, err := NewKVService(host, 1).ListAll(context.Background(), "")
	require.NoError(t, err)
	assert.Empty(t, keys)
}

// TestMVCCKVService_ListAll_ReturnsWhatListFilters covers the MVCC engine,
// which reaches the same answer through a range scan rather than a prefix
// query.
func TestMVCCKVService_ListAll_ReturnsWhatListFilters(t *testing.T) {
	ctx := context.Background()
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return &MVCCRangeResult{KeyValues: []*KeyValue{
				{Key: []byte(tsKey)},
				{Key: []byte(userKey)},
			}}, nil
		},
	}
	svc := NewMVCCKVService(host, 1)

	filtered, err := svc.List(ctx, "")
	require.NoError(t, err)
	assert.Equal(t, []string{userKey}, filtered)

	all, err := svc.ListAll(ctx, "")
	require.NoError(t, err)
	assert.Equal(t, []string{tsKey, userKey}, all)
}

// TestMVCCKVService_ListAll_ScopesToThePrefix pins that dropping the filter
// does not also drop the scope: the range handed to the engine must still be
// bounded by the requested prefix.
func TestMVCCKVService_ListAll_ScopesToThePrefix(t *testing.T) {
	var gotStart, gotEnd []byte
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, query interface{}) (interface{}, error) {
			q, ok := query.(MVCCRangeQuery)
			if !ok {
				t.Fatalf("query = %T, want MVCCRangeQuery", query)
			}
			gotStart, gotEnd = q.Start, q.End
			return &MVCCRangeResult{}, nil
		},
	}

	_, err := NewMVCCKVService(host, 1).ListAll(context.Background(), "ts:acme/")
	require.NoError(t, err)
	assert.Equal(t, "ts:acme/", string(gotStart))
	assert.Equal(t, "ts:acme0", string(gotEnd), "the range must end at the prefix successor")
}

// TestMVCCKVService_ListAll_PropagatesAReadFailure is the error path.
func TestMVCCKVService_ListAll_PropagatesAReadFailure(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, errors.New("read failed")
		},
	}

	_, err := NewMVCCKVService(host, 1).ListAll(context.Background(), "")
	require.Error(t, err)
}

// TestMVCCKVService_ListAll_RefusesAnUnexpectedResult covers the type guard.
func TestMVCCKVService_ListAll_RefusesAnUnexpectedResult(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return "not a range result", nil
		},
	}

	_, err := NewMVCCKVService(host, 1).ListAll(context.Background(), "")
	var want *ReadError
	require.ErrorAs(t, err, &want)
}

// TestBackendKVService_ListAll_ReturnsWhatListFilters covers the direct
// storage-backend engine, the one a dedicated-store database is served from.
func TestBackendKVService_ListAll_ReturnsWhatListFilters(t *testing.T) {
	ctx := context.Background()
	svc, cleanup := newBackendKVService(t)
	defer cleanup()

	require.NoError(t, svc.Put(ctx, tsKey, []byte("v")))
	require.NoError(t, svc.Put(ctx, userKey, []byte("v")))

	filtered, err := svc.List(ctx, "")
	require.NoError(t, err)
	assert.Equal(t, []string{userKey}, filtered)

	// Sorted, unlike the two engines above, which return the order the state
	// machine gave them. BackendKVService.List already sorts; ListAll matches
	// it so the two differ only in the filter.
	all, err := svc.ListAll(ctx, "")
	require.NoError(t, err)
	assert.Equal(t, []string{userKey, tsKey}, all)
}

// TestBackendKVService_ListAll_ScopesToThePrefix is the peer of the MVCC
// scoping test.
func TestBackendKVService_ListAll_ScopesToThePrefix(t *testing.T) {
	ctx := context.Background()
	svc, cleanup := newBackendKVService(t)
	defer cleanup()

	require.NoError(t, svc.Put(ctx, tsKey, []byte("v")))
	require.NoError(t, svc.Put(ctx, timeseries.KeyPrefixPoints+"other/db/cpu:1", []byte("v")))

	all, err := svc.ListAll(ctx, timeseries.KeyPrefixPoints+"acme/")
	require.NoError(t, err)
	assert.Equal(t, []string{tsKey}, all)
}
