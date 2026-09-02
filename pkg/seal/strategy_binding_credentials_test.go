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
	"testing"
)

// TestSoftwareStrategyBindingModes_CredentialSemantics is the enforcement half
// of the doc warnings on NewSoftwareStrategy (pkg/seal/strategy.go and
// sdk/go/types_seal.go). Those comments assert that the two constructors have
// OPPOSITE credential semantics; this pins the assertion so a comment cannot
// drift away from the code it describes, and so an upstream change to either
// mode is caught here rather than by the next security review.
//
// go-qrdb#162 was filed because nothing enforced the distinction. The cost of
// that was measured three times: the #157 production bypass, a pkg/database
// suite that stayed green over a barrier checking nothing, and a #158 fixture
// whose wrong-passphrase assertion went red because the bound strategy it was
// built on accepted the wrong passphrase.
//
// The two subtests below are the two halves, and they fail in opposite
// directions on purpose. Note which is which: a REJECTION assertion goes red
// against a bound fixture (it cannot be written there), while a SUCCESS-only
// assertion passes against both constructors and is the one that proves
// nothing.
func TestSoftwareStrategyBindingModes_CredentialSemantics(t *testing.T) {
	ctx := context.Background()
	correct := Credentials{Secret: []byte("correct-horse-battery-staple")}
	wrong := Credentials{Secret: []byte("wrong-horse-battery-staple")}
	rootKey := bytes.Repeat([]byte{0xA5}, 32)

	t.Run("bound ignores unseal credentials", func(t *testing.T) {
		strategy, err := NewSoftwareStrategy(correct.Secret)
		if err != nil {
			t.Fatalf("NewSoftwareStrategy: %v", err)
		}
		defer func() { _ = strategy.Close() }()

		sealed, err := strategy.SealRootKey(ctx, rootKey, correct)
		if err != nil {
			t.Fatalf("SealRootKey: %v", err)
		}

		// This is the footgun, asserted deliberately. A bound strategy
		// derives from the passphrase bound at construction, so the
		// credentials handed to UnsealRootKey are unused: the WRONG secret
		// returns the root key and a nil error. A caller must not read that
		// nil as proof the credentials were correct.
		got, err := strategy.UnsealRootKey(ctx, sealed, wrong)
		if err != nil {
			t.Fatalf("bound UnsealRootKey with the WRONG secret returned %v; "+
				"the constructor's doc warning says it succeeds regardless of "+
				"credentials. If go-quicraft changed the semantics, update the "+
				"warnings on both NewSoftwareStrategy aliases", err)
		}
		if !bytes.Equal(got, rootKey) {
			t.Fatal("bound UnsealRootKey with the WRONG secret returned a different root key")
		}
	})

	t.Run("deferred validates unseal credentials", func(t *testing.T) {
		strategy := NewDeferredSoftwareStrategy()
		defer func() { _ = strategy.Close() }()

		sealed, err := strategy.SealRootKey(ctx, rootKey, correct)
		if err != nil {
			t.Fatalf("SealRootKey: %v", err)
		}

		if got, err := strategy.UnsealRootKey(ctx, sealed, correct); err != nil {
			t.Fatalf("deferred UnsealRootKey with the correct secret: %v", err)
		} else if !bytes.Equal(got, rootKey) {
			t.Fatal("deferred UnsealRootKey with the correct secret returned a different root key")
		}

		// The reason to prefer this constructor: the credential is actually
		// checked, so this assertion is falsifiable — it holds here, and it is
		// the very assertion that goes red against a bound fixture. That is
		// what makes the deferred constructor the only one a rejection test
		// can be written on.
		if _, err := strategy.UnsealRootKey(ctx, sealed, wrong); err == nil {
			t.Fatal("deferred UnsealRootKey with the WRONG secret succeeded; " +
				"the deferred constructor is the one that authenticates the " +
				"unsealer, and every wrong-credential test in this repo relies on it")
		}
	})
}
