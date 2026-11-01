// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package sealtest

import (
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

func TestPassphraseChangeInFlight_RefusesASecondChangeWhileHeld(t *testing.T) {
	ctx := t.Context()
	barrier, release := PassphraseChangeInFlight(t, "first", "second")

	err := barrier.ChangePassword(ctx, seal.MustCredentials(ctx, []byte("first")), seal.MustCredentials(ctx, []byte("third")))
	if !errors.Is(err, seal.ErrRootKeyWriteInProgress) {
		t.Fatalf("second ChangePassword = %v, want ErrRootKeyWriteInProgress", err)
	}
	release()
}

func TestPassphraseChangeInFlight_ReleaseLandsTheHeldChange(t *testing.T) {
	ctx := t.Context()
	barrier, release := PassphraseChangeInFlight(t, "first", "second")
	release()
	release()

	// The held change wrote "second" and gave up the rewrite: a change from
	// "first" is now a mismatch, and one from "second" goes through.
	err := barrier.ChangePassword(ctx, seal.MustCredentials(ctx, []byte("first")), seal.MustCredentials(ctx, []byte("third")))
	if !errors.Is(err, seal.ErrCredentialMismatch) {
		t.Fatalf("ChangePassword from the replaced passphrase = %v, want ErrCredentialMismatch", err)
	}
	if err := barrier.ChangePassword(ctx, seal.MustCredentials(ctx, []byte("second")), seal.MustCredentials(ctx, []byte("third"))); err != nil {
		t.Fatalf("ChangePassword from the held change's passphrase: %v", err)
	}
}
