// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package seal

import (
	"context"
	"errors"
	"testing"
)

// --------------------------------------------------------------------------
// strategy_shamir.go: UnsealRootKey — 0%
// --------------------------------------------------------------------------

func TestShamirStrategy_UnsealRootKey_AlwaysErrors(t *testing.T) {
	strategy, err := NewShamirStrategy(2, 3)
	if err != nil {
		t.Fatal(err)
	}
	_, err = strategy.UnsealRootKey(context.Background(), &SealedRootKey{}, Credentials{})
	if !errors.Is(err, ErrShamirUnsealRequiresQuorum) {
		t.Errorf("UnsealRootKey: got %v, want ErrShamirUnsealRequiresQuorum", err)
	}
}

func TestShamirStrategy_Close(t *testing.T) {
	strategy, err := NewShamirStrategy(2, 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := strategy.Close(); err != nil {
		t.Fatalf("ShamirStrategy.Close: %v", err)
	}
}
