package sdk_test

import (
	"context"
	"fmt"
	"os"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	sdkstorage "github.com/jeremyhahn/go-qrdb/sdk/go/storage"
)

// Account is an entity with a unique secondary index on Email.
type Account struct {
	ID    uint64 `json:"id"`
	Email string `json:"email" index:"unique"`
}

func (a *Account) EntityID() uint64      { return a.ID }
func (a *Account) SetEntityID(id uint64) { a.ID = id }

// ExampleNewEmbeddedKVStore builds a DAO with a unique index over the
// embedder's own Pebble storage, with no node.
func ExampleNewEmbeddedKVStore() {
	dir, err := os.MkdirTemp("", "dao-store-example")
	if err != nil {
		fmt.Println(err)
		return
	}
	defer func() { _ = os.RemoveAll(dir) }()

	stg, err := sdkstorage.NewPebbleStorage(sdkstorage.DefaultPebbleConfig(dir))
	if err != nil {
		fmt.Println(err)
		return
	}
	defer func() { _ = stg.Close() }()

	store, err := sdk.NewEmbeddedKVStore(stg)
	if err != nil {
		fmt.Println(err)
		return
	}
	accounts, err := sdk.NewDAO(store, "account", func() *Account { return &Account{} })
	if err != nil {
		fmt.Println(err)
		return
	}

	ctx := context.Background()
	if err := accounts.Save(ctx, &Account{ID: 1, Email: "ada@example.test"}); err != nil {
		fmt.Println(err)
		return
	}
	found, err := accounts.QueryOneByIndex(ctx, "Email", "ada@example.test")
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(found.ID, found.Email)

	// The unique index refuses a second account with the same email.
	err = accounts.Save(ctx, &Account{ID: 2, Email: "ada@example.test"})
	fmt.Println(err != nil)
	// Output:
	// 1 ada@example.test
	// true
}
