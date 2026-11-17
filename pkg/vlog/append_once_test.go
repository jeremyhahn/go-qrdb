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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// requestFaultReader is a real storage.Reader except for the iterator over
// the request records, which fails: with iterErr from Error, or with
// closeErr from Close.
type requestFaultReader struct {
	storage.Reader
	iterErr  error
	closeErr error
}

func (r requestFaultReader) NewIterator(opts *storage.IteratorOptions) storage.Iterator {
	if opts == nil || !bytes.Equal(opts.LowerBound, []byte(requestPrefix)) {
		return r.Reader.NewIterator(opts)
	}
	if r.iterErr != nil {
		return errIterator{err: r.iterErr}
	}
	return closeFaultIterator{Iterator: r.Reader.NewIterator(opts), err: r.closeErr}
}

// closeFaultIterator is a real iterator whose Close fails with err.
type closeFaultIterator struct {
	storage.Iterator
	err error
}

func (it closeFaultIterator) Close() error {
	_ = it.Iterator.Close()
	return it.err
}

func TestRequestKey_Layout(t *testing.T) {
	got := requestKey("acme", "tok")
	want := []byte(TablePrefix + "r/acme/tok")
	if !bytes.Equal(got, want) {
		t.Fatalf("requestKey = %q, want %q", got, want)
	}
	if bytes.HasPrefix(got, []byte(headPrefix)) || bytes.HasPrefix(got, []byte(eventPrefix)) {
		t.Fatalf("requestKey %q falls inside the head or event range", got)
	}
}

func TestChain_AppendOnce_RepeatTokenAppendsNothing(t *testing.T) {
	store := memory.NewStorage()
	c := NewChain()

	seq, appended, err := c.AppendOnce(store, "acme", "onboard", fixedRecord(0), false)
	if err != nil || !appended || seq != 1 {
		t.Fatalf("first AppendOnce = %d, %v, %v; want 1, true, nil", seq, appended, err)
	}
	if _, err := store.Get(requestKey("acme", "onboard")); err != nil {
		t.Fatalf("request marker not stored: %v", err)
	}

	seq, appended, err = c.AppendOnce(store, "acme", "onboard", fixedRecord(1), false)
	if err != nil || appended || seq != 1 {
		t.Fatalf("repeat AppendOnce = %d, %v, %v; want 1, false, nil", seq, appended, err)
	}
	if n := countEvents(t, store, "acme"); n != 1 {
		t.Fatalf("events after a repeat = %d, want 1", n)
	}

	// Another token, and the same token on another tenant, are new events.
	if seq, appended, err = c.AppendOnce(store, "acme", "other", fixedRecord(1), false); err != nil || !appended || seq != 2 {
		t.Fatalf("other token = %d, %v, %v; want 2, true, nil", seq, appended, err)
	}
	if seq, appended, err = c.AppendOnce(store, "globex", "onboard", fixedRecord(0), false); err != nil || !appended || seq != 1 {
		t.Fatalf("other tenant = %d, %v, %v; want 1, true, nil", seq, appended, err)
	}
}

func TestChain_AppendOnce_AuditAppendsTheInstanceEvent(t *testing.T) {
	store := memory.NewStorage()
	c := NewChain()

	seq, appended, err := c.AppendOnce(store, auditTenant, "onboard", linked(0), true)
	if err != nil || !appended || seq != 1 {
		t.Fatalf("audit AppendOnce = %d, %v, %v; want 1, true, nil", seq, appended, err)
	}
	seq, appended, err = c.AppendOnce(store, auditTenant, "onboard", linked(1), true)
	if err != nil || appended || seq != 1 {
		t.Fatalf("repeat audit AppendOnce = %d, %v, %v; want 1, false, nil", seq, appended, err)
	}
	// The next distinct event extends the same instance.
	if _, err := c.AppendAudit(store, auditTenant, linked(1)); err != nil {
		t.Fatalf("AppendAudit after AppendOnce: %v", err)
	}
}

// A replica restored from the table after the first proposal applied (the
// emitter's retry arrives late) refuses the retry as the original did.
func TestChain_AppendOnce_RestoreKeepsTheRequestSet(t *testing.T) {
	store := memory.NewStorage()
	first := NewChain()
	if _, appended, err := first.AppendOnce(store, "acme", "onboard", fixedRecord(0), false); err != nil || !appended {
		t.Fatalf("first AppendOnce = %v, %v", appended, err)
	}
	if _, err := first.Append(store, "acme", fixedRecord(1)); err != nil {
		t.Fatalf("Append: %v", err)
	}

	restored := NewChain()
	if err := restored.Restore(store); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	seq, appended, err := restored.AppendOnce(store, "acme", "onboard", fixedRecord(2), false)
	if err != nil || appended || seq != 2 {
		t.Fatalf("delayed repeat after Restore = %d, %v, %v; want 2, false, nil", seq, appended, err)
	}
	if n := countEvents(t, store, "acme"); n != 2 {
		t.Fatalf("events after the delayed repeat = %d, want 2", n)
	}

	// Restoring from an empty table drops the set: the token is new there.
	empty := memory.NewStorage()
	if err := restored.Restore(empty); err != nil {
		t.Fatalf("Restore(empty): %v", err)
	}
	if _, appended, err := restored.AppendOnce(empty, "acme", "onboard", fixedRecord(0), false); err != nil || !appended {
		t.Fatalf("AppendOnce after an empty Restore = %v, %v; want true, nil", appended, err)
	}
}

func TestChain_AppendOnce_NilRequestSet(t *testing.T) {
	c := NewChain()
	c.requests = nil
	store := memory.NewStorage()
	if _, appended, err := c.AppendOnce(store, "acme", "onboard", fixedRecord(0), false); err != nil || !appended {
		t.Fatalf("AppendOnce = %v, %v; want true, nil", appended, err)
	}
	if _, appended, err := c.AppendOnce(store, "acme", "onboard", fixedRecord(1), false); err != nil || appended {
		t.Fatalf("repeat AppendOnce = %v, %v; want false, nil", appended, err)
	}
}

func TestChain_AppendOnce_RefusesTenantIDContainingSlash(t *testing.T) {
	c := NewChain()
	_, appended, err := c.AppendOnce(memory.NewStorage(), "a/b", "onboard", fixedRecord(0), false)
	var invalid *InvalidTenantIDError
	if !errors.As(err, &invalid) || appended {
		t.Fatalf("AppendOnce(a/b) = %v, %v; want *InvalidTenantIDError, false", appended, err)
	}
}

func TestChain_AppendOnce_TableWriteFault(t *testing.T) {
	for name, failSet := range map[string][]byte{
		"event record":   []byte(eventPrefix),
		"request record": []byte(requestPrefix),
	} {
		t.Run(name, func(t *testing.T) {
			c := NewChain()
			w := failingWriter{Writer: memory.NewStorage(), failSet: failSet}
			seq, appended, err := c.AppendOnce(w, "acme", "onboard", fixedRecord(0), false)
			var twe *TableWriteError
			if !errors.As(err, &twe) || !errors.Is(err, errStorageFault) || appended || seq != 0 {
				t.Fatalf("AppendOnce = %d, %v, %v; want 0, false, *TableWriteError", seq, appended, err)
			}
			if _, ok := c.requests[string(requestKey("acme", "onboard"))]; ok {
				t.Fatal("a failed AppendOnce recorded its request")
			}
		})
	}
}

func TestChain_Restore_RequestIteratorFaults(t *testing.T) {
	store := memory.NewStorage()
	if _, _, err := NewChain().AppendOnce(store, "acme", "onboard", fixedRecord(0), false); err != nil {
		t.Fatalf("AppendOnce: %v", err)
	}
	for name, r := range map[string]requestFaultReader{
		"iterator error": {Reader: store, iterErr: errStorageFault},
		"close error":    {Reader: store, closeErr: errStorageFault},
	} {
		t.Run(name, func(t *testing.T) {
			c := NewChain()
			err := c.Restore(r)
			var tre *TableReadError
			if !errors.As(err, &tre) || !errors.Is(err, errStorageFault) {
				t.Fatalf("Restore() err = %v, want *TableReadError wrapping %v", err, errStorageFault)
			}
			if len(c.requests) != 0 {
				t.Fatal("a failed Restore installed a request set")
			}
		})
	}
}
