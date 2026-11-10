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
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// The credential service's replicated store keeps every record under
// "__sys:credentials/": a value, a retained version and the metadata. The
// embedded decorators confine every one of them, not only a "material"
// record.
const credentialsRoot = InternalKeyPrefixSystem + "credentials/"

var credentialRecords = []string{
	credentialsRoot + "db-pass",
	credentialsRoot + "db-pass#meta",
	credentialsRoot + "db-pass#v1",
}

func TestHardConfinedKeys_TheCredentialPrefixIsConfinedWhole(t *testing.T) {
	for _, key := range credentialRecords {
		if !isHardConfinedKey(key) {
			t.Errorf("isHardConfinedKey(%q) = false, want true", key)
		}
	}
	if !isHardConfinedRoot(credentialsRoot) {
		t.Errorf("isHardConfinedRoot(%q) = false, want true", credentialsRoot)
	}
	if isHardConfinedKey(InternalKeyPrefixSystem + "credentialsX/db-pass") {
		t.Error("a sibling of the credential prefix is confined")
	}
	// A CA's certificate record stays public: whole-prefix confinement does
	// not widen the CA prefixes.
	if isHardConfinedKey(hardConfinedMaterialPrefixes[0] + "certificate") {
		t.Error("a CA certificate record is confined")
	}
}

func TestHardConfinedKV_RefusesACredentialPointReadAndWrite(t *testing.T) {
	ctx := context.Background()
	fake := newFakeKV()
	for _, key := range credentialRecords {
		fake.store[key] = []byte("secret")
	}
	confined := NewHardConfinedKV(fake)
	for _, key := range credentialRecords {
		if _, err := confined.Get(ctx, key); err == nil {
			t.Fatalf("Get(%q) succeeded, want a refusal", key)
		} else {
			assertHardConfinedError(t, err)
		}
		if err := confined.Put(ctx, key, []byte("replaced")); err == nil {
			t.Fatalf("Put(%q) succeeded, want a refusal", key)
		} else {
			assertHardConfinedError(t, err)
		}
		if err := confined.Delete(ctx, key); err == nil {
			t.Fatalf("Delete(%q) succeeded, want a refusal", key)
		} else {
			assertHardConfinedError(t, err)
		}
		if string(fake.store[key]) != "secret" {
			t.Fatalf("%q changed to %q despite every write being refused", key, fake.store[key])
		}
	}
}

func TestHardConfinedKV_RefusesAndFiltersACredentialScan(t *testing.T) {
	ctx := context.Background()
	fake := newFakeKV()
	for _, key := range credentialRecords {
		fake.store[key] = []byte("secret")
	}
	fake.store["ordinary/a"] = []byte("1")
	confined := NewHardConfinedKV(fake)
	for _, prefix := range []string{credentialsRoot, credentialsRoot + "db"} {
		if _, err := confined.Scan(ctx, prefix); err == nil {
			t.Fatalf("Scan(%q) succeeded, want a refusal", prefix)
		} else {
			assertHardConfinedError(t, err)
		}
		if _, err := confined.List(ctx, prefix); err == nil {
			t.Fatalf("List(%q) succeeded, want a refusal", prefix)
		} else {
			assertHardConfinedError(t, err)
		}
	}
	for _, prefix := range []string{"", InternalKeyPrefixSystem} {
		pairs, err := confined.Scan(ctx, prefix)
		if err != nil {
			t.Fatalf("Scan(%q): %v", prefix, err)
		}
		for _, key := range credentialRecords {
			if _, ok := pairs[key]; ok {
				t.Fatalf("Scan(%q) returned the credential record %q", prefix, key)
			}
		}
	}
}

func TestHardConfinedMVCC_FiltersCredentialsFromARange(t *testing.T) {
	ctx := context.Background()
	fake := newFakeMVCC()
	for _, key := range credentialRecords {
		fake.store[key] = []byte("secret")
	}
	fake.store["ordinary/a"] = []byte("1")
	mvcc := NewHardConfinedMVCC(fake)

	if _, _, _, err := mvcc.Range(ctx, []byte(credentialRecords[0]), nil); err == nil {
		t.Fatal("a point Range on a credential record succeeded, want a refusal")
	} else {
		assertHardConfinedError(t, err)
	}
	kvs, count, _, err := mvcc.Range(ctx, []byte(""), []byte("\xff"), WithRevision(1))
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if count != 1 || len(kvs) != 1 || string(kvs[0].Key) != "ordinary/a" {
		t.Fatalf("Range = %v (count %d), want exactly the non-credential key", kvs, count)
	}
}

func TestHardConfinedBatch_RefusesACredentialOp(t *testing.T) {
	ctx := context.Background()
	fake := &fakeBatch{}
	batch := NewHardConfinedBatch(fake)
	for _, ops := range [][]Op{
		{{Type: OpGet, Key: []byte(credentialRecords[0])}},
		{{Type: OpPut, Key: []byte(credentialRecords[1]), Value: []byte("{}")}},
	} {
		if _, err := batch.Execute(ctx, ops); err == nil {
			t.Fatal("a batch op against a credential record succeeded, want a refusal")
		} else {
			assertHardConfinedError(t, err)
		}
	}
	if fake.called {
		t.Fatal("the batch reached the underlying servicer despite the refusal")
	}
}

func TestHardConfinedTransaction_RefusesACredentialCompareOrOp(t *testing.T) {
	ctx := context.Background()
	fake := &fakeTxn{}
	txn := NewHardConfinedTransaction(fake)
	compares := []Compare{{Key: []byte(credentialRecords[1]), Target: CompareVersion, Result: CompareEqual}}
	if _, err := txn.Execute(ctx, compares, nil, nil); err == nil {
		t.Fatal("a compare against a credential record succeeded, want a refusal")
	} else {
		assertHardConfinedError(t, err)
	}
	ops := []Op{{Type: OpPut, Key: []byte(credentialRecords[0]), Value: []byte("replaced")}}
	if _, err := txn.Execute(ctx, nil, ops, nil); err == nil {
		t.Fatal("an op against a credential record succeeded, want a refusal")
	} else {
		assertHardConfinedError(t, err)
	}
	if fake.called {
		t.Fatal("the transaction reached the underlying servicer despite the refusal")
	}
}

func TestHardConfinedWatch_RefusesAndFiltersCredentials(t *testing.T) {
	ctx := context.Background()
	fake := &fakeWatch{}
	w := NewHardConfinedWatch(fake)
	if _, err := w.Watch(ctx, credentialRecords[0]); err == nil {
		t.Fatal("Watch on a credential record succeeded, want a refusal")
	} else {
		assertHardConfinedError(t, err)
	}
	if fake.calledWatch {
		t.Fatal("Watch delegated to the underlying servicer despite the refusal")
	}

	watcher, err := w.WatchPrefix(ctx, "")
	if err != nil {
		t.Fatalf("WatchPrefix(\"\"): %v", err)
	}
	fake.prefixWatcher.events <- watch.WatchResponse{
		WatchID:  1,
		Revision: 1,
		Events: []*watch.Event{
			{Type: watch.EventPut, Kv: &kv.KeyValue{Key: []byte(credentialRecords[0]), Value: []byte("secret")}},
			{Type: watch.EventPut, Kv: &kv.KeyValue{Key: []byte("ordinary/a"), Value: []byte("1")}},
		},
	}
	fake.prefixWatcher.Close()
	select {
	case resp, ok := <-watcher.Events():
		if !ok {
			t.Fatal("Events() closed before delivering the one response pushed")
		}
		if len(resp.Events) != 1 || string(resp.Events[0].Kv.Key) != "ordinary/a" {
			t.Fatalf("WatchPrefix delivered %v, want exactly the non-credential event", resp.Events)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the filtered watch event")
	}
	if err := watcher.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// The credential migration claims sit beside the credential store, under
// "__sys:credential-migrations/". A claim names the copy a migration settled
// on, so a write through the embedded KV would redirect or replay a
// migration: the prefix is refused exactly as a credential value key is.
const credentialMigrationsRoot = InternalKeyPrefixSystem + "credential-migrations/"

func TestHardConfinedKeys_TheCredentialMigrationPrefixIsConfinedWhole(t *testing.T) {
	ctx := context.Background()
	key := credentialMigrationsRoot + "db-pass"
	if !isHardConfinedKey(key) {
		t.Errorf("isHardConfinedKey(%q) = false, want true", key)
	}
	if !isHardConfinedRoot(credentialMigrationsRoot) {
		t.Errorf("isHardConfinedRoot(%q) = false, want true", credentialMigrationsRoot)
	}

	fake := newFakeKV()
	fake.store[key] = []byte("claim")
	confined := NewHardConfinedKV(fake)
	if _, err := confined.Get(ctx, key); err == nil {
		t.Fatalf("Get(%q) succeeded, want a refusal", key)
	} else {
		assertHardConfinedError(t, err)
	}
	if err := confined.Put(ctx, key, []byte("replaced")); err == nil {
		t.Fatalf("Put(%q) succeeded, want a refusal", key)
	} else {
		assertHardConfinedError(t, err)
	}
	if string(fake.store[key]) != "claim" {
		t.Fatalf("%q changed to %q despite the write being refused", key, fake.store[key])
	}
	if _, err := confined.Scan(ctx, credentialMigrationsRoot); err == nil {
		t.Fatalf("Scan(%q) succeeded, want a refusal", credentialMigrationsRoot)
	} else {
		assertHardConfinedError(t, err)
	}
	pairs, err := confined.Scan(ctx, InternalKeyPrefixSystem)
	if err != nil {
		t.Fatalf("Scan(%q): %v", InternalKeyPrefixSystem, err)
	}
	if _, ok := pairs[key]; ok {
		t.Fatalf("Scan(%q) returned the migration claim %q", InternalKeyPrefixSystem, key)
	}

	fakeTx := &fakeTxn{}
	txn := NewHardConfinedTransaction(fakeTx)
	compares := []Compare{{Key: []byte(key), Target: CompareVersion, Result: CompareEqual}}
	if _, err := txn.Execute(ctx, compares, nil, nil); err == nil {
		t.Fatal("a compare against a migration claim succeeded, want a refusal")
	} else {
		assertHardConfinedError(t, err)
	}
	ops := []Op{{Type: OpPut, Key: []byte(key), Value: []byte("replaced")}}
	if _, err := txn.Execute(ctx, nil, ops, nil); err == nil {
		t.Fatal("an op against a migration claim succeeded, want a refusal")
	} else {
		assertHardConfinedError(t, err)
	}
	if fakeTx.called {
		t.Fatal("the transaction reached the underlying servicer despite the refusal")
	}

	fakeW := &fakeWatch{}
	w := NewHardConfinedWatch(fakeW)
	if _, err := w.Watch(ctx, key); err == nil {
		t.Fatal("Watch on a migration claim succeeded, want a refusal")
	} else {
		assertHardConfinedError(t, err)
	}
	if fakeW.calledWatch {
		t.Fatal("Watch delegated to the underlying servicer despite the refusal")
	}

	// A prefix watch is filtered, as it is for a credential record.
	watcher, err := w.WatchPrefix(ctx, credentialMigrationsRoot)
	if err != nil {
		t.Fatalf("WatchPrefix(%q): %v", credentialMigrationsRoot, err)
	}
	fakeW.prefixWatcher.events <- watch.WatchResponse{
		WatchID:  1,
		Revision: 1,
		Events: []*watch.Event{
			{Type: watch.EventPut, Kv: &kv.KeyValue{Key: []byte(key), Value: []byte("claim")}},
			{Type: watch.EventPut, Kv: &kv.KeyValue{Key: []byte("ordinary/a"), Value: []byte("1")}},
		},
	}
	fakeW.prefixWatcher.Close()
	select {
	case resp, ok := <-watcher.Events():
		if !ok {
			t.Fatal("Events() closed before delivering the one response pushed")
		}
		if len(resp.Events) != 1 || string(resp.Events[0].Kv.Key) != "ordinary/a" {
			t.Fatalf("WatchPrefix delivered %v, want exactly the non-claim event", resp.Events)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the filtered watch event")
	}
	if err := watcher.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// The credential service's own store lists and scans its prefix through the
// node's KV service, whose internal-key filter keeps the records for a scan
// rooted at the prefix and strips them from a broader "__sys:" sweep.
func TestFilterInternalKeys_KeepsCredentialsForTheStoresOwnScan(t *testing.T) {
	got := FilterInternalKeys(append([]string(nil), credentialRecords...), credentialsRoot)
	if len(got) != len(credentialRecords) {
		t.Fatalf("FilterInternalKeys(%q) = %v, want every credential record", credentialsRoot, got)
	}
	keys := append([]string{InternalKeyPrefixSystem + "roles/admin"}, credentialRecords...)
	got = FilterInternalKeys(append([]string(nil), keys...), InternalKeyPrefixSystem)
	if len(got) != 1 || got[0] != InternalKeyPrefixSystem+"roles/admin" {
		t.Fatalf("FilterInternalKeys(%q) = %v, want the credential records stripped", InternalKeyPrefixSystem, got)
	}

	data := map[string][]byte{}
	for _, key := range credentialRecords {
		data[key] = []byte("v")
	}
	if m := FilterInternalMap(data, credentialsRoot); len(m) != len(credentialRecords) {
		t.Fatalf("FilterInternalMap(%q) kept %d records, want %d", credentialsRoot, len(m), len(credentialRecords))
	}
}
