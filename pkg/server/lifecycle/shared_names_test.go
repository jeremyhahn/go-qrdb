package lifecycle

import (
	"slices"
	"testing"
)

// TestSharedNames proves the Infra.SharedDatabaseNames source lists the SQL
// default database and the registry's names at call time, deduplicated,
// without SystemDatabase or empty names, and tolerates a registry that does
// not exist yet.
func TestSharedNames(t *testing.T) {
	var registry []string
	names := SharedNames("sqldefault", func() []string { return registry })
	if got := names(); !slices.Equal(got, []string{"sqldefault"}) {
		t.Fatalf("SharedNames before the registry = %v, want [sqldefault]", got)
	}
	registry = []string{SystemDatabase, "registered", "sqldefault", "", "registered"}
	if got := names(); !slices.Equal(got, []string{"sqldefault", "registered"}) {
		t.Fatalf("SharedNames after the registry = %v, want [sqldefault registered]", got)
	}
	if got := SharedNames("", nil)(); len(got) != 0 {
		t.Fatalf("SharedNames with no default and no registry = %v, want none", got)
	}
	if got := SharedNames(SystemDatabase, nil)(); len(got) != 0 {
		t.Fatalf("SharedNames with the system default = %v, want none", got)
	}
}
