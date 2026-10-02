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

package auth_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

// TestSOAuthenticator_KnownGoodSurvivesARestart: a tenant SO that verified
// its PIN is persisted as known-good in the authenticator's store, so a new
// authenticator over the same store -- a restart -- lets it through even
// once wrong PINs from many other principals have held the aggregate cap.
func TestSOAuthenticator_KnownGoodSurvivesARestart(t *testing.T) {
	store := newSOMemKV()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	first, err := auth.NewSOAuthenticator(store, &soNoopAudit{}, logger)
	if err != nil {
		t.Fatal(err)
	}
	so := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "so", TenantID: "acme"})
	if err := first.SetSOPIN(so, "acme", []byte("correct-horse-battery-staple")); err != nil {
		t.Fatal(err)
	}
	if err := first.VerifySOPIN(so, "acme", []byte("correct-horse-battery-staple")); err != nil {
		t.Fatalf("the SO's first verification: %v", err)
	}

	restarted, err := auth.NewSOAuthenticator(store, &soNoopAudit{}, logger)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < auth.SOPINAggregateFailures; i++ {
		other := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "user-" + string(rune('a'+i/auth.SOPINFreeFailures)), TenantID: "acme"})
		_ = restarted.VerifySOPIN(other, "acme", []byte("wrong-pin-0000"))
	}
	var locked *auth.SOPINLockedError
	stranger := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "stranger", TenantID: "acme"})
	if err := restarted.VerifySOPIN(stranger, "acme", []byte("correct-horse-battery-staple")); !errors.As(err, &locked) {
		t.Fatalf("a principal never known good under the held cap = %v, want locked", err)
	}
	if err := restarted.VerifySOPIN(so, "acme", []byte("correct-horse-battery-staple")); err != nil {
		t.Fatalf("the persisted known-good SO after a restart = %v, want admitted", err)
	}
}
