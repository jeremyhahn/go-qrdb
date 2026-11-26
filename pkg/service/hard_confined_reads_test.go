package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
)

// TestHardConfinedKV_StaleAndConsistentReadsGuardTheConfinedKey covers the
// stale and consistency-option enumeration methods.
func TestHardConfinedKV_StaleAndConsistentReadsGuardTheConfinedKey(t *testing.T) {
	ctx := context.Background()
	fake := newFakeKV()
	fake.store[confinedKey] = []byte("secret")
	fake.store[InternalKeyPrefixSystem+"roles/x"] = []byte(`{"name":"x"}`)
	kv := NewHardConfinedKV(fake)
	opts := consistency.ReadOptions{}

	lists := map[string]func(string) ([]string, error){
		"list stale": func(p string) ([]string, error) { return kv.ListStale(ctx, p) },
		"list consistent": func(p string) ([]string, error) {
			return kv.ListWithConsistency(ctx, p, opts)
		},
	}
	for name, list := range lists {
		keys, err := list("")
		require.NoError(t, err, name)
		require.Equal(t, []string{InternalKeyPrefixSystem + "roles/x"}, keys, name)
		_, err = list(confinedKey)
		require.Error(t, err, name)
	}

	scans := map[string]func(string) (map[string][]byte, error){
		"scan stale": func(p string) (map[string][]byte, error) { return kv.ScanStale(ctx, p) },
		"scan consistent": func(p string) (map[string][]byte, error) {
			return kv.ScanWithConsistency(ctx, p, opts)
		},
	}
	for name, scan := range scans {
		data, err := scan("")
		require.NoError(t, err, name)
		require.Len(t, data, 1, name)
		require.NotContains(t, data, confinedKey, name)
		_, err = scan(confinedKey)
		require.Error(t, err, name)
	}
}
