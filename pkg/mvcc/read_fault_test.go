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

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// errReadFault is the fault readFaultStorage raises.
var errReadFault = errors.New("injected read fault")

// readFaultStorage fails Get for keys carrying a chosen prefix, leaving every
// other operation intact. It models a transient local read fault.
type readFaultStorage struct {
	storage.Storage
	failPrefix []byte
	armed      bool
}

func (s *readFaultStorage) Get(key []byte) ([]byte, error) {
	if s.armed && len(s.failPrefix) > 0 && len(key) >= len(s.failPrefix) &&
		string(key[:len(s.failPrefix)]) == string(s.failPrefix) {
		return nil, errReadFault
	}
	return s.Storage.Get(key)
}

// A key that cannot be READ is not a key that is ABSENT.
//
// getFrom used to report every metadata-read failure as KeyNotFoundError, so
// an apply-path existence guard was told a key it could not read did not
// exist. On the conditional-put path that meant overwriting the holder and
// reporting ResultStored — and because such a fault is LOCAL to one replica,
// replicas whose reads succeeded would decide the same log entry the other
// way and the group would diverge. getMetadataFrom always distinguished the
// two; getFrom was throwing the distinction away.
func TestStore_Get_ReadFaultIsNotReportedAsAbsence(t *testing.T) {
	inner := memory.NewStorage()
	t.Cleanup(func() { _ = inner.Close() })

	stg := &readFaultStorage{Storage: inner, failPrefix: EncodeMetaKey([]byte("k"))}
	s, err := New(stg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, perr := s.Put([]byte("k"), []byte("v"), 0); perr != nil {
		t.Fatalf("Put: %v", perr)
	}

	stg.armed = true
	_, gerr := s.Get([]byte("k"))
	if gerr == nil {
		t.Fatal("Get succeeded while its metadata read was failing")
	}
	var notFound *KeyNotFoundError
	if errors.As(gerr, &notFound) {
		t.Fatalf("Get reported a read fault as KeyNotFoundError (%v) — an existence "+
			"guard reading that answer overwrites the holder and diverges replicas", gerr)
	}
	if !errors.Is(gerr, errReadFault) {
		t.Fatalf("Get = %v, want the underlying read fault propagated", gerr)
	}

	// A genuinely missing key still reports absence, or every conditional put
	// would stall instead of storing.
	stg.armed = false
	if _, mErr := s.Get([]byte("absent")); !errors.As(mErr, &notFound) {
		t.Fatalf("Get(absent) = %v, want KeyNotFoundError", mErr)
	}
}
