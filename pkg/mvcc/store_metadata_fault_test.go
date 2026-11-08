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

package mvcc

import (
	"errors"
	"testing"
)

func TestFindAtRevisionFrom_CorruptMetadataIsAnError(t *testing.T) {
	s, mem := newCorruptibleStore(t)
	b := mem.Batch()
	if err := b.Set(EncodeMetaKey([]byte("b")), []byte{0xff}); err != nil {
		t.Fatalf("corrupt the metadata entry: %v", err)
	}
	if err := b.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	_ = b.Close()

	kv, err := s.findAtRevisionFrom(mem, []byte("b"), s.CurrentRevision())
	var nf *KeyNotFoundError
	if err == nil || errors.As(err, &nf) {
		t.Fatalf("findAtRevisionFrom = (%+v, %v), want a metadata fault that is not KeyNotFoundError", kv, err)
	}
	if _, _, _, err := s.RangeFrom(mem, s.CurrentRevision(), []byte("a"), []byte("z")); err == nil || errors.As(err, &nf) {
		t.Fatalf("RangeFrom = %v, want the metadata fault to fail the range", err)
	}
}

func TestFindAtRevisionFrom_AbsentKeyIsAbsent(t *testing.T) {
	s, mem := newCorruptibleStore(t)
	if _, err := s.findAtRevisionFrom(mem, []byte("missing"), s.CurrentRevision()); !absentAtRevision(err) {
		t.Fatalf("findAtRevisionFrom(missing) = %v, want an absence, not a fault", err)
	}
}
