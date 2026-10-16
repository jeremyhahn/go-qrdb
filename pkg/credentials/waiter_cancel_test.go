// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package credentials

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

// TestRetrieveManual_CancelDeregistersWaiter verifies that a manual-credential
// retrieve cancelled before submission deregisters its waiter (so a later
// submit does not copy the credential into an abandoned channel) and that the
// service remains correct afterward.
func TestRetrieveManual_CancelDeregistersWaiter(t *testing.T) {
	svc, err := NewCredentialService(
		&Config{Strategy: StrategyManual}, nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if err != nil {
		t.Fatalf("NewCredentialService: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, e := svc.retrieveBytes(ctx, "db")
		done <- e
	}()

	// Wait until the slow-path waiter is registered.
	if !waitUntil(func() bool {
		svc.waitersMu.Lock()
		defer svc.waitersMu.Unlock()
		return len(svc.waiters["db"]) == 1
	}) {
		t.Fatal("waiter was never registered")
	}

	cancel()
	if e := <-done; e == nil {
		t.Fatal("expected a context error from the cancelled retrieve")
	}

	// The waiter must be deregistered on cancellation.
	svc.waitersMu.Lock()
	n := len(svc.waiters["db"])
	svc.waitersMu.Unlock()
	if n != 0 {
		t.Fatalf("waiter not deregistered after cancel: %d remain", n)
	}

	// A subsequent submit must not panic, and the credential is retrievable.
	if e := svc.SubmitCredential(context.Background(), "db", []byte("secret")); e != nil {
		t.Fatalf("SubmitCredential: %v", e)
	}
	got, e := svc.retrieveBytes(context.Background(), "db")
	if e != nil {
		t.Fatalf("RetrieveCredential: %v", e)
	}
	if string(got) != "secret" {
		t.Errorf("retrieved %q, want %q", got, "secret")
	}
}

// waitUntil polls cond up to ~2s, returning true once it holds.
func waitUntil(cond func() bool) bool {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return cond()
}
