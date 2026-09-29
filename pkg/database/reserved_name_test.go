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

package database

import (
	"errors"
	"testing"
)

func TestIsReservedName(t *testing.T) {
	for name, want := range map[string]bool{
		TenantHostWALDirName:      true,
		TenantHostNodeHostDirName: true,
		"WAL":                     true,
		"NodeHost":                true,
		"wal2":                    false,
		"ledger":                  false,
	} {
		if got := IsReservedName(name); got != want {
			t.Errorf("IsReservedName(%q) = %v, want %v", name, got, want)
		}
	}
}

// Every create path validates the configuration first, so a reserved name is
// refused before any directory is made.
func TestManager_Create_ReservedName(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()
	for _, name := range []string{"wal", "nodehost"} {
		_, err := mgr.Create(validMemoryConfig(name))
		var reserved *ReservedNameError
		if !errors.As(err, &reserved) || reserved.Name != name {
			t.Fatalf("Create(%q) error = %v (%T), want *ReservedNameError", name, err, err)
		}
	}
	if (&ReservedNameError{Name: "wal"}).Error() != `database: database name is reserved: "wal"` {
		t.Errorf("unexpected message: %q", (&ReservedNameError{Name: "wal"}).Error())
	}
}
