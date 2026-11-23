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

//go:build qrdb_vlog

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
)

// TestBackendKV_RefusesABarrierDatabasesLogKeyspace proves the kv surface of
// a barrier database cannot write into the verifiable log keyspace. The
// database's logs live in its parent group (owner ruling 1234), so its
// backend holds none, and the keyspace stays reserved: get, put,
// put-if-absent, compare-and-swap, compare-and-delete, exists and delete of
// a reserved key are refused with a ReservedKeyError, list, list-all and
// scan never show one, a delete range over them is refused, and the log
// stays readable.
func TestBackendKV_RefusesABarrierDatabasesLogKeyspace(t *testing.T) {
	dir := t.TempDir()
	mgr := newBarrierDatabaseManager(t, dir)
	svc, _ := newParentGroupLogService(t, newFenceGroup(t), mgr, nil)
	ctx := acmeCtx()
	ref := LogRef{Database: "secure", Log: "events"}
	created, err := svc.CreateLog(ctx, &CreateLogRequest{LogRef: ref, Origin: "example.test/secure/events", SigningKeyType: vlog.SignatureEd25519})
	if err != nil {
		t.Fatalf("CreateLog: %v", err)
	}
	entries := [][]byte{[]byte("reserved-entry-0001"), []byte("reserved-entry-0002")}
	if _, err := svc.Append(ctx, &AppendRequest{LogRef: ref, Entries: entries}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	db, err := mgr.GetOwned("secure", "acme")
	if err != nil {
		t.Fatalf("GetOwned: %v", err)
	}
	bg := context.Background()
	raw, err := db.Backend().List(bg, "")
	if err != nil {
		t.Fatalf("backend List: %v", err)
	}
	var reserved []string
	for _, k := range raw {
		if vlog.IsTableKey([]byte(k)) {
			reserved = append(reserved, k)
		}
	}
	if len(reserved) != 0 {
		t.Fatalf("the barrier database's backend holds log records %q; want them in the parent group", reserved)
	}
	// The keyspace stays reserved: a key a log record would take is refused.
	reserved = []string{vlog.LogTablePrefix + "secure/events/h", vlog.LogTablePrefix}

	kv := NewNamedBackendKVService("secure", db.Backend())
	if err := kv.Put(bg, "app/visible", []byte("v")); err != nil {
		t.Fatalf("Put of a user key: %v", err)
	}
	refused := func(op string, err error) {
		t.Helper()
		var rk *ReservedKeyError
		if !errors.As(err, &rk) {
			t.Errorf("%s = %v; want a ReservedKeyError", op, err)
		}
	}
	for _, k := range reserved {
		_, err := kv.Get(bg, k)
		refused("Get", err)
		_, err = kv.GetStale(bg, k)
		refused("GetStale", err)
		refused("Put", kv.Put(bg, k, []byte("forged")))
		_, err = kv.PutIfAbsent(bg, k, []byte("forged"))
		refused("PutIfAbsent", err)
		_, err = kv.CompareAndSwap(bg, k, nil, []byte("forged"))
		refused("CompareAndSwap", err)
		_, err = kv.CompareAndDelete(bg, k, nil)
		refused("CompareAndDelete", err)
		refused("Delete", kv.Delete(bg, k))
	}
	listed, err := kv.List(bg, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	all, err := kv.ListAll(bg, "")
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	scanned, err := kv.Scan(bg, "")
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	for _, keys := range [][]string{listed, all} {
		for _, k := range keys {
			if vlog.IsTableKey([]byte(k)) {
				t.Errorf("list shows reserved key %q", k)
			}
		}
	}
	for k := range scanned {
		if vlog.IsTableKey([]byte(k)) {
			t.Errorf("scan shows reserved key %q", k)
		}
	}
	if _, ok := scanned["app/visible"]; !ok {
		t.Errorf("scan = %v; want the user key", scanned)
	}
	for _, r := range [][2][]byte{{nil, nil}, {[]byte(vlog.LogTablePrefix), nil}, {[]byte("\x00"), []byte("\x01")}} {
		refused("DeleteRange", kv.DeleteRange(bg, r[0], r[1]))
	}
	if err := kv.DeleteRange(bg, []byte("app/"), []byte("app0")); err != nil {
		t.Fatalf("DeleteRange of user keys: %v", err)
	}
	if _, err := kv.Get(bg, "app/visible"); err == nil {
		t.Fatal("the user key survived its delete range")
	}

	head, err := svc.Head(ctx, &HeadRequest{LogRef: ref})
	if err != nil || head.Head.Size != uint64(len(entries)) {
		t.Fatalf("Head = %+v, %v; want size %d", head, err, len(entries))
	}
	key, err := (&logKeyReader{route: svc.(*logService).route}).ReadSigningKey(ctx, created.ID, created.Key.Version)
	if err != nil || key == nil || len(key.PKCS8) == 0 {
		t.Fatalf("ReadSigningKey = %v, %v; want the log's key record", key, err)
	}
}

func TestRefuseReservedRange(t *testing.T) {
	cases := []struct {
		start, end string
		refused    bool
	}{
		{"a", "b", false},
		{"", "\x00", false},
		{"", "\x00qrdb.audit/", false},
		{"", "\x00qrdb.audit/x", true},
		{"\x00qrdb.vlog/", "", true},
		{"\x00qrdb.vlog0", "", false},
		{"\x00qrdb.audit0", "\x00qrdb.vlog/", false},
		{"\x00qrdb.audit0", "\x00qrdb.vlog/a", true},
	}
	for _, c := range cases {
		err := refuseReservedRange([]byte(c.start), []byte(c.end))
		if (err != nil) != c.refused {
			t.Errorf("refuseReservedRange(%q, %q) = %v; want refused %v", c.start, c.end, err, c.refused)
		}
	}
}
