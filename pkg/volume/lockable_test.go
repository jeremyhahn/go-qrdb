// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package volume

import (
	"bytes"
	"errors"
	"testing"
)

func TestDeriveVolumeKey_DeterministicForSameInputs(t *testing.T) {
	masterKey := bytes.Repeat([]byte{0x42}, 32)

	k1, err := DeriveVolumeKey(masterKey, "node-1", "fast")
	if err != nil {
		t.Fatalf("DeriveVolumeKey: %v", err)
	}
	k2, err := DeriveVolumeKey(masterKey, "node-1", "fast")
	if err != nil {
		t.Fatalf("DeriveVolumeKey: %v", err)
	}
	if k1 != k2 {
		t.Fatalf("DeriveVolumeKey is not deterministic: %x != %x", k1, k2)
	}
}

func TestDeriveVolumeKey_DiffersByNodeAndPool(t *testing.T) {
	masterKey := bytes.Repeat([]byte{0x42}, 32)

	base, err := DeriveVolumeKey(masterKey, "node-1", "fast")
	if err != nil {
		t.Fatalf("DeriveVolumeKey: %v", err)
	}
	byNode, err := DeriveVolumeKey(masterKey, "node-2", "fast")
	if err != nil {
		t.Fatalf("DeriveVolumeKey: %v", err)
	}
	byPool, err := DeriveVolumeKey(masterKey, "node-1", "bulk")
	if err != nil {
		t.Fatalf("DeriveVolumeKey: %v", err)
	}

	if base == byNode {
		t.Fatalf("two nodes derived the same key for the same pool")
	}
	if base == byPool {
		t.Fatalf("two pools derived the same key on the same node")
	}
}

func TestDeriveVolumeKey_EmptyMasterKey(t *testing.T) {
	_, err := DeriveVolumeKey(nil, "node-1", "fast")
	var target *InvalidMasterKeyError
	if !errors.As(err, &target) {
		t.Fatalf("DeriveVolumeKey(nil, ...) error = %v, want *InvalidMasterKeyError", err)
	}
}

func TestDeriveVolumeKey_EmptyIdentity(t *testing.T) {
	masterKey := bytes.Repeat([]byte{0x42}, 32)

	cases := []struct {
		name   string
		nodeID string
		pool   string
	}{
		{"empty node id", "", "fast"},
		{"empty pool", "node-1", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := DeriveVolumeKey(masterKey, c.nodeID, c.pool)
			var target *InvalidVolumeIdentityError
			if !errors.As(err, &target) {
				t.Fatalf("DeriveVolumeKey error = %v, want *InvalidVolumeIdentityError", err)
			}
		})
	}
}

func TestVolumeKey_WipeZeroesInPlace(t *testing.T) {
	masterKey := bytes.Repeat([]byte{0x42}, 32)
	key, err := DeriveVolumeKey(masterKey, "node-1", "fast")
	if err != nil {
		t.Fatalf("DeriveVolumeKey: %v", err)
	}
	if key == (VolumeKey{}) {
		t.Fatal("derived key was all zeros before Wipe, test is not exercising anything")
	}
	key.Wipe()
	if key != (VolumeKey{}) {
		t.Fatalf("Wipe did not zero the key: %x", key)
	}
	// Wipe is safe to call again.
	key.Wipe()
}
