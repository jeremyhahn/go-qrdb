package database

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

var errClaimSwap = errors.New("system store: claim swap failed")

// A create in an isolated tenant's home whose record write committed but
// whose root-claim commit failed leaves no catalog record and no claim: the
// manager reports the failure, nothing resolves the name, and a retry of the
// same name succeeds instead of being refused as AlreadyExists.
func TestManager_CreateClaimCommitFailureLeavesNoRecord(t *testing.T) {
	system := &swapHookStore{MemoryKVStore: kvstore.NewMemoryKVStore()}
	home := kvstore.NewMemoryKVStore()
	cat, err := NewKVCatalog(system, &isolatedHomes{homes: map[string]kvstore.KVStore{"iso": home}})
	if err != nil {
		t.Fatalf("NewKVCatalog: %v", err)
	}
	mgr := NewManager(nil, t.TempDir())
	mgr.SetCatalog(cat)
	t.Cleanup(func() { _ = mgr.Close() })
	ctx := tenantContext("iso")

	system.swapErr = errClaimSwap
	if _, err := mgr.CreateForTenant(ctx, validMemoryConfig("orders")); !errors.Is(err, errClaimSwap) {
		t.Fatalf("CreateForTenant with a failing claim commit = %v; want the claim error", err)
	}
	var notFound *NotFoundError
	if _, err := cat.Get(context.Background(), "iso", "orders"); !errors.As(err, &notFound) {
		t.Fatalf("catalog Get after the failed create = %v; want *NotFoundError", err)
	}
	if _, err := mgr.GetForTenant(ctx, "orders"); err == nil {
		t.Fatal("the failed database resolves through the manager")
	}
	var claims []string
	if err := system.Scan(context.Background(), RootClaimKeyPrefix, func(key string, _ []byte) error {
		claims = append(claims, key)
		return nil
	}); err != nil {
		t.Fatalf("Scan claims: %v", err)
	}
	if len(claims) != 0 {
		t.Fatalf("the failed create left root claims %v", claims)
	}

	system.swapErr = nil
	if _, err := mgr.CreateForTenant(ctx, validMemoryConfig("orders")); err != nil {
		t.Fatalf("retry of the failed create: %v", err)
	}
	if _, err := cat.Get(context.Background(), "iso", "orders"); err != nil {
		t.Fatalf("catalog Get after the retry: %v", err)
	}
}
