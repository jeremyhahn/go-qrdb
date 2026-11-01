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

package store

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/cockroachdb/pebble/v2/vfs/errorfs"

	sm "github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// newValueFetchFaultMVCCSM opens an MVCC state machine whose Pebble keeps
// large values in blob files, and whose filesystem fails every blob file
// read while the returned flag is set. Key enumeration and metadata reads
// stay on the sstables, so only the lazy value fetch fails.
func newValueFetchFaultMVCCSM(t *testing.T) (*MVCCStateMachine, *atomic.Bool) {
	t.Helper()
	var armed atomic.Bool
	fs := errorfs.Wrap(vfs.NewMem(), errorfs.InjectorFunc(func(op errorfs.Op) error {
		if armed.Load() && strings.HasSuffix(op.Path, ".blob") && (op.Kind == errorfs.OpFileRead ||
			op.Kind == errorfs.OpFileReadAt || op.Kind == errorfs.OpOpen) {
			return errorfs.ErrInjected
		}
		return nil
	}))
	opts := &pebble.Options{
		FS:                 fs,
		Cache:              pebble.NewCache(1),
		FormatMajorVersion: pebble.FormatNewest,
	}
	opts.Experimental.ValueSeparationPolicy = func() pebble.ValueSeparationPolicy {
		return pebble.ValueSeparationPolicy{
			Enabled:               true,
			MinimumSize:           64,
			MaxBlobReferenceDepth: 10,
			TargetGarbageRatio:    1.0,
		}
	}
	s := NewMVCCStateMachine(opts)
	ctx := context.Background()
	if _, err := s.Open(ctx, t.TempDir(), make(chan struct{})); err != nil {
		t.Fatalf("open mvcc sm: %v", err)
	}
	t.Cleanup(func() {
		armed.Store(false)
		_ = s.Close(ctx)
	})
	return s, &armed
}

// TestMVCCStateMachine_RangeReportsValueFetchFault fails the lazy value
// fetch of an existing key under a Range and checks the range fails: a
// fault read as absence answers a short range that omits the key.
func TestMVCCStateMachine_RangeReportsValueFetchFault(t *testing.T) {
	big := bytes.Repeat([]byte("v"), 4096)
	for _, tc := range []struct {
		name  string
		query service.MVCCRangeQuery
	}{
		{name: "single key", query: service.MVCCRangeQuery{Start: []byte("a")}},
		{name: "enumeration", query: service.MVCCRangeQuery{Start: []byte("a"), End: []byte("z")}},
		{name: "count only", query: service.MVCCRangeQuery{Start: []byte("a"), End: []byte("z"), CountOnly: true, Revision: 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, armed := newValueFetchFaultMVCCSM(t)
			applyMVCCEntries(t, s, []sm.Entry{
				mvccPutEntry(1, []byte("a"), big, 0),
				mvccPutEntry(2, []byte("b"), big, 0),
			})
			if err := s.db.Flush(); err != nil {
				t.Fatalf("flush: %v", err)
			}
			// The state is read through the published view; republish it so
			// the view reads the flushed tables rather than the memtable.
			applyMVCCEntries(t, s, []sm.Entry{mvccPutEntry(3, []byte("c"), []byte("small"), 0)})

			armed.Store(true)
			got, err := s.Lookup(context.Background(), tc.query)
			armed.Store(false)

			if !errors.Is(err, errorfs.ErrInjected) {
				t.Fatalf("Lookup(%+v) = %+v, %v; want the value fetch fault, not a "+
					"range that omits an existing key", tc.query, got, err)
			}
		})
	}
}
