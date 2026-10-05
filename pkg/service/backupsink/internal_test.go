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

// White-box (package backupsink, not backupsink_test) so these tests can
// swap marshalJSON -- an unexported indirection -- to force Write's
// otherwise-unreachable encode-failure branches.
package backupsink

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/objstore"
)

// swapMarshalJSON installs fn as marshalJSON for the duration of the
// calling test, restoring the real encoding/json.Marshal on cleanup.
func swapMarshalJSON(t *testing.T, fn func(any) ([]byte, error)) {
	t.Helper()
	prev := marshalJSON
	marshalJSON = fn
	t.Cleanup(func() { marshalJSON = prev })
}

func newMemStoreForTest(t *testing.T) *Sink {
	t.Helper()
	store, err := objstore.New("memory", map[string]string{})
	if err != nil {
		t.Fatalf("objstore.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return New(store)
}

// TestSink_Write_InfoEncodeFailureReturnsEncodeError forces the
// info-marshal branch (otherwise unreachable against the real encoder:
// every service.BackupInfo field marshals successfully) and proves it
// surfaces as *EncodeError.
func TestSink_Write_InfoEncodeFailureReturnsEncodeError(t *testing.T) {
	sink := newMemStoreForTest(t)
	boom := errors.New("boom")
	swapMarshalJSON(t, func(v any) ([]byte, error) {
		if _, ok := v.(service.BackupInfo); ok {
			return nil, boom
		}
		return json.Marshal(v)
	})

	err := sink.Write(context.Background(), service.BackupInfo{ID: "fails-info"}, map[uint64][]byte{1: []byte("x")}, nil)
	var encErr *EncodeError
	if !errors.As(err, &encErr) {
		t.Fatalf("Write err = %T, want *EncodeError", err)
	}
	if !errors.Is(err, boom) {
		t.Fatalf("Write err = %v, want it to wrap the forced marshal failure", err)
	}
}

// TestSink_Write_EnvelopeEncodeFailureReturnsEncodeError forces the
// envelope-marshal branch the same way, and proves the already-written
// shard payload is rolled back (best-effort) rather than left orphaned.
func TestSink_Write_EnvelopeEncodeFailureReturnsEncodeError(t *testing.T) {
	sink := newMemStoreForTest(t)
	boom := errors.New("boom")
	swapMarshalJSON(t, func(v any) ([]byte, error) {
		if _, ok := v.(*service.BackupEnvelope); ok {
			return nil, boom
		}
		return json.Marshal(v)
	})

	info := service.BackupInfo{ID: "fails-envelope", Encryption: service.EncryptionAES256}
	envelope := &service.BackupEnvelope{ProviderID: "software"}
	err := sink.Write(context.Background(), info, map[uint64][]byte{1: []byte("x")}, envelope)
	var encErr *EncodeError
	if !errors.As(err, &encErr) {
		t.Fatalf("Write err = %T, want *EncodeError", err)
	}
	if !errors.Is(err, boom) {
		t.Fatalf("Write err = %v, want it to wrap the forced marshal failure", err)
	}

	// The shard payload Write already persisted before the envelope
	// marshal failed must be rolled back -- a subsequent ReadShards must
	// not find an orphaned payload for a backup that was never listed.
	if _, err := sink.ReadShards(context.Background(), info.ID); err == nil {
		t.Fatal("ReadShards succeeded for a backup whose Write failed; the orphaned payload was not rolled back")
	}
}
