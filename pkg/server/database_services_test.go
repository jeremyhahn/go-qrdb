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

package server

import (
	"sort"
	"sync"
	"testing"
)

// newTestDatabaseServices builds a minimal DatabaseServices for use in tests.
// Only Name and ShardID are populated; service fields are left nil because
// the registry does not inspect them.
func newTestDatabaseServices(name string, shardID uint64) *DatabaseServices {
	return &DatabaseServices{
		Name:    name,
		ShardID: shardID,
	}
}

func TestNewDatabaseServiceRegistry(t *testing.T) {
	t.Parallel()

	reg := NewDatabaseServiceRegistry("default")

	if reg == nil {
		t.Fatal("expected non-nil registry")
	}
	if reg.DefaultName() != "default" {
		t.Errorf("DefaultName() = %q, want %q", reg.DefaultName(), "default")
	}
	if reg.Count() != 0 {
		t.Errorf("Count() = %d, want 0", reg.Count())
	}
}

func TestRegisterAndGet(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		dbName    string
		shardID   uint64
		wantFound bool
	}{
		{
			name:      "registered database is found",
			dbName:    "mydb",
			shardID:   42,
			wantFound: true,
		},
		{
			name:      "unregistered database returns nil",
			dbName:    "missing",
			shardID:   0,
			wantFound: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			reg := NewDatabaseServiceRegistry("default")
			if tc.wantFound {
				svc := newTestDatabaseServices(tc.dbName, tc.shardID)
				reg.Register(tc.dbName, svc)
			}

			got := reg.Get(tc.dbName)
			if tc.wantFound {
				if got == nil {
					t.Fatalf("Get(%q) returned nil, want non-nil", tc.dbName)
				}
				if got.Name != tc.dbName {
					t.Errorf("Name = %q, want %q", got.Name, tc.dbName)
				}
				if got.ShardID != tc.shardID {
					t.Errorf("ShardID = %d, want %d", got.ShardID, tc.shardID)
				}
			} else {
				if got != nil {
					t.Errorf("Get(%q) = %v, want nil", tc.dbName, got)
				}
			}
		})
	}
}

func TestUnregister(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		registerDB string
		removeDB   string
		wantCount  int
	}{
		{
			name:       "unregister existing database removes it",
			registerDB: "alpha",
			removeDB:   "alpha",
			wantCount:  0,
		},
		{
			name:       "unregister absent database is a no-op",
			registerDB: "alpha",
			removeDB:   "beta",
			wantCount:  1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			reg := NewDatabaseServiceRegistry("default")
			reg.Register(tc.registerDB, newTestDatabaseServices(tc.registerDB, 1))
			reg.Unregister(tc.removeDB)

			if reg.Count() != tc.wantCount {
				t.Errorf("Count() = %d, want %d", reg.Count(), tc.wantCount)
			}
			if tc.wantCount == 0 {
				if got := reg.Get(tc.registerDB); got != nil {
					t.Errorf("Get(%q) after unregister = %v, want nil", tc.registerDB, got)
				}
			}
		})
	}
}

func TestGetOrDefault(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		lookup   string
		wantName string
	}{
		{
			name:     "empty name falls back to default",
			lookup:   "",
			wantName: "default",
		},
		{
			name:     "explicit name returns that database",
			lookup:   "custom",
			wantName: "custom",
		},
		{
			name:     "unknown name returns nil",
			lookup:   "nonexistent",
			wantName: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			reg := NewDatabaseServiceRegistry("default")
			reg.Register("default", newTestDatabaseServices("default", 1))
			reg.Register("custom", newTestDatabaseServices("custom", 2))

			got := reg.GetOrDefault(tc.lookup)
			if tc.wantName == "" {
				if got != nil {
					t.Errorf("GetOrDefault(%q) = %v, want nil", tc.lookup, got)
				}
				return
			}
			if got == nil {
				t.Fatalf("GetOrDefault(%q) returned nil, want name=%q", tc.lookup, tc.wantName)
			}
			if got.Name != tc.wantName {
				t.Errorf("Name = %q, want %q", got.Name, tc.wantName)
			}
		})
	}
}

func TestDefault(t *testing.T) {
	t.Parallel()

	t.Run("default database present", func(t *testing.T) {
		t.Parallel()

		reg := NewDatabaseServiceRegistry("default")
		svc := newTestDatabaseServices("default", 1)
		reg.Register("default", svc)

		got := reg.Default()
		if got == nil {
			t.Fatal("Default() returned nil, want non-nil")
		}
		if got.Name != "default" {
			t.Errorf("Name = %q, want %q", got.Name, "default")
		}
	})

	t.Run("default database absent returns nil", func(t *testing.T) {
		t.Parallel()

		reg := NewDatabaseServiceRegistry("default")
		if got := reg.Default(); got != nil {
			t.Errorf("Default() = %v, want nil for empty registry", got)
		}
	})
}

func TestList(t *testing.T) {
	t.Parallel()

	t.Run("empty registry", func(t *testing.T) {
		t.Parallel()

		reg := NewDatabaseServiceRegistry("default")
		names := reg.List()
		if len(names) != 0 {
			t.Errorf("List() = %v, want empty slice", names)
		}
	})

	t.Run("multiple databases listed", func(t *testing.T) {
		t.Parallel()

		reg := NewDatabaseServiceRegistry("default")
		reg.Register("alpha", newTestDatabaseServices("alpha", 1))
		reg.Register("beta", newTestDatabaseServices("beta", 2))
		reg.Register("gamma", newTestDatabaseServices("gamma", 3))

		names := reg.List()
		if len(names) != 3 {
			t.Fatalf("List() returned %d names, want 3", len(names))
		}
		sort.Strings(names)
		want := []string{"alpha", "beta", "gamma"}
		for i, n := range names {
			if n != want[i] {
				t.Errorf("names[%d] = %q, want %q", i, n, want[i])
			}
		}
	})

	t.Run("unregister removes from list", func(t *testing.T) {
		t.Parallel()

		reg := NewDatabaseServiceRegistry("default")
		reg.Register("alpha", newTestDatabaseServices("alpha", 1))
		reg.Register("beta", newTestDatabaseServices("beta", 2))
		reg.Unregister("alpha")

		names := reg.List()
		if len(names) != 1 || names[0] != "beta" {
			t.Errorf("List() = %v, want [beta]", names)
		}
	})
}

func TestCount(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		registers []string
		removes   []string
		want      int
	}{
		{
			name: "zero after no registrations",
			want: 0,
		},
		{
			name:      "three after three registrations",
			registers: []string{"a", "b", "c"},
			want:      3,
		},
		{
			name:      "two after register three and remove one",
			registers: []string{"a", "b", "c"},
			removes:   []string{"b"},
			want:      2,
		},
		{
			name:      "duplicate registration counts once",
			registers: []string{"a", "a"},
			want:      1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			reg := NewDatabaseServiceRegistry("default")
			for _, name := range tc.registers {
				reg.Register(name, newTestDatabaseServices(name, 1))
			}
			for _, name := range tc.removes {
				reg.Unregister(name)
			}
			if got := reg.Count(); got != tc.want {
				t.Errorf("Count() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestConcurrentAccess(t *testing.T) {
	t.Parallel()

	const (
		numWriters = 8
		numReaders = 8
		numOps     = 200
	)

	reg := NewDatabaseServiceRegistry("default")
	reg.Register("default", newTestDatabaseServices("default", 1))

	var wg sync.WaitGroup

	// Writers: register and unregister databases concurrently.
	for i := range numWriters {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			name := "db-writer-" + string(rune('a'+id))
			for range numOps {
				reg.Register(name, newTestDatabaseServices(name, uint64(id+10)))
				reg.Unregister(name)
			}
		}(i)
	}

	// Readers: read default and arbitrary names concurrently.
	for range numReaders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range numOps {
				_ = reg.Default()
				_ = reg.GetOrDefault("")
				_ = reg.GetOrDefault("nonexistent")
				_ = reg.List()
				_ = reg.Count()
				_ = reg.DefaultName()
			}
		}()
	}

	wg.Wait()

	// The default database must still be intact after concurrent writes.
	got := reg.Default()
	if got == nil {
		t.Fatal("Default() returned nil after concurrent access")
	}
	if got.Name != "default" {
		t.Errorf("Default().Name = %q, want %q", got.Name, "default")
	}
}
