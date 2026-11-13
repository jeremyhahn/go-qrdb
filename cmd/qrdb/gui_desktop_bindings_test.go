//go:build desktop

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

package main

import (
	"reflect"
	"testing"
)

// expectedBindingCount is the number of bindings that must be present in
// the Bind list returned by guiBindings.bindList(). Update this constant
// whenever a binding is added or removed; the mismatch will be caught here
// before a blank-screen runtime error reaches users.
//
// Current manifest (32 bindings):
//
//	ConnectionBinding, ConnectionManagerBinding, EmbeddedServerBinding,
//	KVBinding, AdminBinding, MVCCBinding, TransactionBinding,
//	BatchBinding, WatchBinding, LeaseBinding, IndexBinding,
//	TimeSeriesBinding, VectorBinding, UserBinding, InvitationBinding,
//	RoleBinding, CertBinding, DatabaseBinding, CollectionBinding, SQLBinding,
//	SealBinding, ConfigBinding, TenantBinding, BackupBinding,
//	OfficerBinding, CeremonyBinding, RebalanceBinding, LockBinding,
//	CredentialBinding, AuditBinding, VolumePoolBinding, VlogBinding, DaemonBinding
const expectedBindingCount = 33

// TestGUIBindings_AllBindingsPresent verifies that bindList returns the
// expected number of non-nil binding instances. A nil entry means a binding
// was added to the struct but accidentally omitted from bindList().
func TestGUIBindings_AllBindingsPresent(t *testing.T) {
	t.Parallel()

	log := testLogger(t)
	b := createGUIBindings(log, "")
	list := b.bindList()

	if len(list) != expectedBindingCount {
		t.Errorf(
			"bindList() returned %d bindings; expected %d — "+
				"update expectedBindingCount or add the missing entry to bindList()",
			len(list), expectedBindingCount,
		)
	}

	for i, entry := range list {
		if entry == nil {
			t.Errorf("bindList()[%d] is nil — a binding was added to the struct but not initialised in createGUIBindings", i)
		}
	}
}

// TestGUIBindings_NilEntries verifies that no element in bindList is a nil
// interface or a typed nil pointer. Wails panics when it encounters a nil
// binding in the Bind slice, which would prevent the application from
// starting at all.
func TestGUIBindings_NilEntries(t *testing.T) {
	t.Parallel()

	log := testLogger(t)
	b := createGUIBindings(log, "")
	list := b.bindList()

	for i, entry := range list {
		if entry == nil {
			t.Errorf("bindList()[%d]: untyped nil", i)
			continue
		}
		v := reflect.ValueOf(entry)
		if v.Kind() == reflect.Ptr && v.IsNil() {
			t.Errorf("bindList()[%d] (%T): typed nil pointer — NewXxxBinding returned nil", i, entry)
		}
	}
}

// TestGUIBindings_TenantBindingPresent is a targeted regression test for the
// 2026-04-06 bug where TenantBinding was created in bindings_tenant.go but
// omitted from the Bind list, making window.go.main.TenantBinding undefined
// at runtime and causing "undefined is not an object (evaluating 't.List')".
func TestGUIBindings_TenantBindingPresent(t *testing.T) {
	t.Parallel()

	log := testLogger(t)
	b := createGUIBindings(log, "")
	list := b.bindList()

	for _, entry := range list {
		if _, ok := entry.(*TenantBinding); ok {
			return // found
		}
	}
	t.Error(
		"TenantBinding not found in bindList() — " +
			"regression of the 2026-04-06 missing binding bug: " +
			"window.go.main.TenantBinding would be undefined at runtime",
	)
}

// TestGUIBindings_BackupBindingPresent is a targeted regression test for the
// 2026-04-06 bug where BackupBinding was created in bindings_backup.go but
// omitted from the Bind list, making window.go.main.BackupBinding undefined
// at runtime.
func TestGUIBindings_BackupBindingPresent(t *testing.T) {
	t.Parallel()

	log := testLogger(t)
	b := createGUIBindings(log, "")
	list := b.bindList()

	for _, entry := range list {
		if _, ok := entry.(*BackupBinding); ok {
			return // found
		}
	}
	t.Error(
		"BackupBinding not found in bindList() — " +
			"regression of the 2026-04-06 missing binding bug: " +
			"window.go.main.BackupBinding would be undefined at runtime",
	)
}

// TestGUIBindings_BindListMatchesStructFields verifies that the count of
// non-nil pointer fields on guiBindings equals the length of bindList,
// catching the case where a field is added to the struct but not returned
// from bindList (or vice versa).
func TestGUIBindings_BindListMatchesStructFields(t *testing.T) {
	t.Parallel()

	log := testLogger(t)
	b := createGUIBindings(log, "")

	// Count exported and unexported pointer fields on guiBindings.
	rv := reflect.ValueOf(*b)
	rt := rv.Type()
	fieldCount := 0
	for i := range rt.NumField() {
		f := rv.Field(i)
		if f.Kind() == reflect.Ptr {
			fieldCount++
		}
	}

	list := b.bindList()
	if len(list) != fieldCount {
		t.Errorf(
			"guiBindings has %d pointer fields but bindList() returns %d entries — "+
				"a field was added to the struct without a corresponding bindList() entry, "+
				"or an entry was added to bindList() without a struct field",
			fieldCount, len(list),
		)
	}
}

// TestGUIBindings_ConnectionBindingPresent verifies the core plumbing
// binding is registered; all other bindings depend on it being available
// for IPC startup lifecycle calls.
func TestGUIBindings_ConnectionBindingPresent(t *testing.T) {
	t.Parallel()

	log := testLogger(t)
	b := createGUIBindings(log, "")
	list := b.bindList()

	for _, entry := range list {
		if _, ok := entry.(*ConnectionBinding); ok {
			return
		}
	}
	t.Error("ConnectionBinding not found in bindList()")
}

// TestGUIBindings_OfficerBindingPresent verifies that OfficerBinding, which
// exposes the security-officer enrollment and share-claim operations to the
// desktop GUI, is included in the Bind list. A missing entry would make
// window.go.main.OfficerBinding undefined at runtime.
func TestGUIBindings_OfficerBindingPresent(t *testing.T) {
	t.Parallel()

	log := testLogger(t)
	b := createGUIBindings(log, "")
	list := b.bindList()

	for _, entry := range list {
		if _, ok := entry.(*OfficerBinding); ok {
			return
		}
	}
	t.Error(
		"OfficerBinding not found in bindList() — " +
			"window.go.main.OfficerBinding would be undefined at runtime",
	)
}

// TestGUIBindings_DaemonBindingPresent verifies that DaemonBinding, which is
// desktop-only and initialised with a logFile path, is included in the list.
func TestGUIBindings_DaemonBindingPresent(t *testing.T) {
	t.Parallel()

	log := testLogger(t)
	b := createGUIBindings(log, "")
	list := b.bindList()

	for _, entry := range list {
		if _, ok := entry.(*DaemonBinding); ok {
			return
		}
	}
	t.Error("DaemonBinding not found in bindList()")
}

// TestGUIBindings_RebalanceBindingPresent verifies that RebalanceBinding,
// which exposes shard rebalancing operations to the desktop GUI, is
// included in the Bind list. A missing entry would make
// window.go.main.RebalanceBinding undefined at runtime.
func TestGUIBindings_RebalanceBindingPresent(t *testing.T) {
	t.Parallel()

	log := testLogger(t)
	b := createGUIBindings(log, "")
	list := b.bindList()

	for _, entry := range list {
		if _, ok := entry.(*RebalanceBinding); ok {
			return
		}
	}
	t.Error(
		"RebalanceBinding not found in bindList() — " +
			"window.go.main.RebalanceBinding would be undefined at runtime",
	)
}

// TestGUIBindings_LockBindingPresent verifies that LockBinding, which
// exposes distributed lock management to the desktop GUI, is included in
// the Bind list. A missing entry would make window.go.main.LockBinding
// undefined at runtime.
func TestGUIBindings_LockBindingPresent(t *testing.T) {
	t.Parallel()

	log := testLogger(t)
	b := createGUIBindings(log, "")
	list := b.bindList()

	for _, entry := range list {
		if _, ok := entry.(*LockBinding); ok {
			return
		}
	}
	t.Error(
		"LockBinding not found in bindList() — " +
			"window.go.main.LockBinding would be undefined at runtime",
	)
}

// TestGUIBindings_CredentialBindingPresent verifies that CredentialBinding,
// which exposes named-credential management to the desktop GUI, is
// included in the Bind list. A missing entry would make
// window.go.main.CredentialBinding undefined at runtime.
func TestGUIBindings_CredentialBindingPresent(t *testing.T) {
	t.Parallel()

	log := testLogger(t)
	b := createGUIBindings(log, "")
	list := b.bindList()

	for _, entry := range list {
		if _, ok := entry.(*CredentialBinding); ok {
			return
		}
	}
	t.Error(
		"CredentialBinding not found in bindList() — " +
			"window.go.main.CredentialBinding would be undefined at runtime",
	)
}

// TestGUIBindings_AuditBindingPresent verifies that AuditBinding, which
// exposes compliance-grade audit event querying to the desktop GUI, is
// included in the Bind list. A missing entry would make
// window.go.main.AuditBinding undefined at runtime.
func TestGUIBindings_AuditBindingPresent(t *testing.T) {
	t.Parallel()

	log := testLogger(t)
	b := createGUIBindings(log, "")
	list := b.bindList()

	for _, entry := range list {
		if _, ok := entry.(*AuditBinding); ok {
			return
		}
	}
	t.Error(
		"AuditBinding not found in bindList() — " +
			"window.go.main.AuditBinding would be undefined at runtime",
	)
}
