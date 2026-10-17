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

package vlog

import (
	"bytes"
	"errors"
	"reflect"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// TestRead_AnswersEveryRequestAsTheDirectRead pins that each read request a
// state machine's Lookup answers through Read returns exactly what the
// storage read it names returns, so the request types add no behaviour of
// their own.
func TestRead_AnswersEveryRequestAsTheDirectRead(t *testing.T) {
	c, stg := mustCreate(t, false)
	mustAppend(t, c, stg, 0, 7)

	root, err := rootAt(c.stagedGet(stg), testLog, 7)
	if err != nil {
		t.Fatalf("rootAt: %v", err)
	}
	if err := c.Checkpoint(stg, stg, testLog, 7, root, []byte("signed-7")); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}

	cases := []struct {
		name string
		req  Request
		want func() (any, error)
	}{
		{"Head", HeadRequest{ID: testLog}, func() (any, error) { return ReadHead(stg, testLog) }},
		{"Get", GetRequest{ID: testLog, From: 2, Limit: 3}, func() (any, error) { return Get(stg, testLog, 2, 3) }},
		{"Inclusion", InclusionRequest{ID: testLog, Index: 3, Size: 7}, func() (any, error) { return Inclusion(stg, testLog, 3, 7) }},
		{"Consistency", ConsistencyRequest{ID: testLog, Size1: 2, Size2: 7}, func() (any, error) { return Consistency(stg, testLog, 2, 7) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Read(stg, tc.req)
			if err != nil {
				t.Fatalf("Read(%T) = %v", tc.req, err)
			}
			want, err := tc.want()
			if err != nil {
				t.Fatalf("direct read: %v", err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("Read(%T) = %#v, want %#v", tc.req, got, want)
			}
		})
	}

	head, err := Read(stg, HeadRequest{ID: testLog})
	if err != nil {
		t.Fatalf("Read(HeadRequest) = %v", err)
	}
	if h := head.(*Head); h.Size != 7 || !bytes.Equal(h.Checkpoint, []byte("signed-7")) {
		t.Fatalf("head = %+v, want size 7 and the signed checkpoint", h)
	}
}

// TestRead_PassesErrorsThrough pins that a request on a missing log, or one
// out of range, answers the storage read's own typed error.
func TestRead_PassesErrorsThrough(t *testing.T) {
	c, stg := mustCreate(t, false)
	mustAppend(t, c, stg, 0, 2)
	missing := LogID{TenantID: testLog.TenantID, Database: testLog.Database, Log: "absent"}

	notFound := []Request{
		HeadRequest{ID: missing},
		GetRequest{ID: missing, Limit: 1},
		InclusionRequest{ID: missing, Index: 0, Size: 1},
		ConsistencyRequest{ID: missing, Size1: 0, Size2: 1},
	}
	for _, req := range notFound {
		var lnf *LogNotFoundError
		if _, err := Read(stg, req); !errors.As(err, &lnf) {
			t.Fatalf("Read(%T) on a missing log = %v, want *LogNotFoundError", req, err)
		}
	}

	var ior *IndexOutOfRangeError
	if _, err := Read(stg, InclusionRequest{ID: testLog, Index: 5, Size: 2}); !errors.As(err, &ior) {
		t.Fatalf("Read(InclusionRequest) past the size = %v, want *IndexOutOfRangeError", err)
	}
	var sor *SizeOutOfRangeError
	if _, err := Read(stg, ConsistencyRequest{ID: testLog, Size1: 1, Size2: 9}); !errors.As(err, &sor) {
		t.Fatalf("Read(ConsistencyRequest) past the head = %v, want *SizeOutOfRangeError", err)
	}
}

// TestReadSnapshot_AnswersAsRead pins that a read through one snapshot
// answers exactly what Read over the live storage answers when nothing
// commits in between, errors included.
func TestReadSnapshot_AnswersAsRead(t *testing.T) {
	c, stg := mustCreate(t, false)
	mustAppend(t, c, stg, 0, 5)
	missing := LogID{TenantID: testLog.TenantID, Database: testLog.Database, Log: "absent"}

	for _, req := range []Request{
		HeadRequest{ID: testLog},
		GetRequest{ID: testLog, From: 1, Limit: 3},
		InclusionRequest{ID: testLog, Index: 2, Size: 5},
		ConsistencyRequest{ID: testLog, Size1: 1, Size2: 5},
	} {
		got, err := ReadSnapshot(stg, req)
		if err != nil {
			t.Fatalf("ReadSnapshot(%T) = %v", req, err)
		}
		want, err := Read(stg, req)
		if err != nil {
			t.Fatalf("Read(%T) = %v", req, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("ReadSnapshot(%T) = %#v, want %#v", req, got, want)
		}
	}
	var lnf *LogNotFoundError
	if _, err := ReadSnapshot(stg, HeadRequest{ID: missing}); !errors.As(err, &lnf) {
		t.Fatalf("ReadSnapshot on a missing log = %v, want *LogNotFoundError", err)
	}
}

// snapshotFault is a Snapshotter whose Snapshot fails, or whose snapshot
// fails to close after serving the read.
type snapshotFault struct {
	storage.Storage
	openErr, closeErr error
}

func (s snapshotFault) Snapshot() (storage.Snapshot, error) {
	if s.openErr != nil {
		return nil, s.openErr
	}
	snap, err := s.Storage.Snapshot()
	if err != nil {
		return nil, err
	}
	return closeFaultSnapshot{Snapshot: snap, err: s.closeErr}, nil
}

// closeFaultSnapshot closes the snapshot it wraps and then reports err.
type closeFaultSnapshot struct {
	storage.Snapshot
	err error
}

func (s closeFaultSnapshot) Close() error {
	if err := s.Snapshot.Close(); err != nil {
		return err
	}
	return s.err
}

// TestReadSnapshot_Faults pins that a snapshot that cannot be taken, or
// cannot be released after the read, is a *TableReadError wrapping the
// fault, and that a close fault never hides the read's own error.
func TestReadSnapshot_Faults(t *testing.T) {
	c, stg := mustCreate(t, false)
	mustAppend(t, c, stg, 0, 2)
	head := HeadRequest{ID: testLog}

	for name, s := range map[string]snapshotFault{
		"open":  {Storage: stg, openErr: errStorageFault},
		"close": {Storage: stg, closeErr: errStorageFault},
	} {
		t.Run(name, func(t *testing.T) {
			var tre *TableReadError
			if _, err := ReadSnapshot(s, head); !errors.As(err, &tre) || !errors.Is(err, errStorageFault) {
				t.Fatalf("ReadSnapshot() = %v, want *TableReadError wrapping the fault", err)
			}
		})
	}

	missing := LogID{TenantID: testLog.TenantID, Database: testLog.Database, Log: "absent"}
	var lnf *LogNotFoundError
	if _, err := ReadSnapshot(snapshotFault{Storage: stg, closeErr: errStorageFault}, HeadRequest{ID: missing}); !errors.As(err, &lnf) {
		t.Fatalf("ReadSnapshot() with a failing read and close = %v, want the read's *LogNotFoundError", err)
	}
}
