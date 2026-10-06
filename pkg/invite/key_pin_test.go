// Copyright (c) 2026 Jeremy Hahn
// Copyright (c) 2026 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
//  1. GNU Affero General Public License v3.0 (AGPL-3.0)
//
//  2. Commercial License
//     Contact licensing@automatethethings.com for commercial licensing options.

package invite

import (
	"context"
	"testing"
	"time"
)

func TestCreate_WithKeyPinPersistsThePin(t *testing.T) {
	store, err := NewTenantStore(newStubResolver())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	token, created, err := store.Create(ctx, "user-1", "tenant-a", time.Hour, WithKeyPin("pin-thumbprint"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.KeyPin != "pin-thumbprint" {
		t.Fatalf("created KeyPin = %q", created.KeyPin)
	}
	got, err := store.Lookup(ctx, "tenant-a", token)
	if err != nil || got.KeyPin != "pin-thumbprint" {
		t.Fatalf("Lookup = %+v, %v; want the pin stored", got, err)
	}
}

func TestCreate_WithoutAPinPinsNothing(t *testing.T) {
	store, err := NewTenantStore(newStubResolver())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	token, _, err := store.Create(ctx, "user-1", "tenant-a", time.Hour, WithKeyPin(""))
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Lookup(ctx, "tenant-a", token)
	if err != nil || got.KeyPin != "" {
		t.Fatalf("Lookup = %+v, %v; want no pin", got, err)
	}
}
