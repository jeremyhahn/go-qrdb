// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package seal

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

func TestRecoveryStrategy_ID(t *testing.T) {
	rs, rsErr := newRecoveryStrategyForTest([]byte("key"), nil)
	if rsErr != nil {
		t.Fatalf("newRecoveryStrategy: %v", rsErr)
	}
	if rs.ID() != StrategyID("recovery") {
		t.Errorf("ID: got %q, want %q", rs.ID(), "recovery")
	}
}

func TestRecoveryStrategy_Available(t *testing.T) {
	rs, rsErr := newRecoveryStrategyForTest([]byte("key"), nil)
	if rsErr != nil {
		t.Fatalf("newRecoveryStrategy: %v", rsErr)
	}
	if !rs.Available() {
		t.Error("Available: expected true")
	}
}

func TestRecoveryStrategy_HardwareBacked(t *testing.T) {
	rs, rsErr := newRecoveryStrategyForTest([]byte("key"), nil)
	if rsErr != nil {
		t.Fatalf("newRecoveryStrategy: %v", rsErr)
	}
	if rs.HardwareBacked() {
		t.Error("HardwareBacked: expected false")
	}
}

func TestRecoveryStrategy_SealRootKey(t *testing.T) {
	rs, rsErr := newRecoveryStrategyForTest([]byte("key"), nil)
	if rsErr != nil {
		t.Fatalf("newRecoveryStrategy: %v", rsErr)
	}
	_, err := rs.SealRootKey(context.Background(), []byte("root"), Credentials{})
	if !errors.Is(err, ErrAlreadyInitialized) {
		t.Errorf("SealRootKey: got %v, want ErrAlreadyInitialized", err)
	}
}

func TestRecoveryStrategy_BarrierEncryptor_NilDelegate(t *testing.T) {
	rs, rsErr := newRecoveryStrategyForTest([]byte("key"), nil)
	if rsErr != nil {
		t.Fatalf("newRecoveryStrategy: %v", rsErr)
	}
	enc, err := rs.BarrierEncryptor(context.Background(), []byte("root"))
	if err != nil {
		t.Fatalf("BarrierEncryptor with nil delegate: %v", err)
	}
	if enc != nil {
		t.Error("expected nil encryptor when delegate is nil")
	}
}

// Close destroys the protected copy the deferred cleanup relies on. Asserted
// directly because the recovery paths depend on it and nothing else in this
// package does.
func TestRecoveryStrategy_CloseWipesItsRootKeyCopy(t *testing.T) {
	key := []byte("root-key-material")
	want := append([]byte(nil), key...)
	recStrat, err := newRecoveryStrategyForTest(key, nil)
	if err != nil {
		t.Fatalf("newRecoveryStrategyForTest: %v", err)
	}

	// The strategy holds a COPY in protected memory: clearing the caller's
	// slice must not change what the strategy protects, or the test would pass
	// against a constructor that aliased the caller's key.
	for i := range key {
		key[i] = 0
	}
	buf, openErr := recStrat.rootKey.Open(t.Context())
	if openErr != nil {
		t.Fatalf("root key should be readable before Close: %v", openErr)
	}
	aliased := !bytes.Equal(bufferContents(buf), want)
	buf.Destroy()
	if aliased {
		t.Fatal("the strategy aliased the caller's key instead of copying it")
	}

	if err := recStrat.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// After Close the protected enclave is destroyed: opening it must fail.
	if _, openErr := recStrat.rootKey.Open(t.Context()); openErr == nil {
		t.Error("Close left the strategy's protected root-key copy readable")
	}
}
