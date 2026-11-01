// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
//  1. GNU Affero General Public License v3.0 (AGPL-3.0)
//
//  2. Commercial License
//     Contact licensing@automatethethings.com for commercial licensing options.

package sdk_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
)

func TestInviteReissueThroughTheSDK(t *testing.T) {
	ctx := context.Background()
	store, err := sdk.NewInviteStore(kvstore.NewMemoryKVStore())
	if err != nil {
		t.Fatalf("NewInviteStore: %v", err)
	}

	if _, _, err := store.Reissue(ctx, "user-1", "acme", time.Hour); !errors.Is(err, sdk.ErrInviteUserNotFound) {
		t.Fatalf("Reissue(uninvited) = %v, want ErrInviteUserNotFound", err)
	}
	old, _, err := store.Create(ctx, "user-1", "acme", time.Hour)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	fresh, _, err := store.Reissue(ctx, "user-1", "acme", time.Hour)
	if err != nil {
		t.Fatalf("Reissue: %v", err)
	}
	if _, err := store.Redeem(ctx, old); !errors.Is(err, sdk.ErrInviteSuperseded) {
		t.Fatalf("Redeem(old) = %v, want ErrInviteSuperseded", err)
	}
	if _, err := store.Redeem(ctx, fresh); err != nil {
		t.Fatalf("Redeem(new): %v", err)
	}
	if err := store.Activate(ctx, "user-1", "acme", time.Now()); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if _, _, err := store.Reissue(ctx, "user-1", "acme", time.Hour); !errors.Is(err, sdk.ErrInviteUserActive) {
		t.Fatalf("Reissue(active) = %v, want ErrInviteUserActive", err)
	}
}
