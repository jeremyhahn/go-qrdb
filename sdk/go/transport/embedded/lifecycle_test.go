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

package embedded

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func newScopedPair(t *testing.T) (root, scoped *Transport) {
	t.Helper()
	root, err := New(newMockKVService(), &mockAdminService{}, WithServiceRegistry(newScopeRegistry()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	st, err := root.WithDatabase("example")
	if err != nil {
		t.Fatalf("WithDatabase: %v", err)
	}
	return root, st.(*Transport)
}

// TestTransport_RootCloseRefusesScopedClones pins that closing the transport
// New built refuses every later call through a database-scoped clone made
// from it, so a client retained across a node's data-plane teardown cannot
// keep writing the seal store through the old barrier.
func TestTransport_RootCloseRefusesScopedClones(t *testing.T) {
	root, scoped := newScopedPair(t)
	ctx := context.Background()
	if err := root.Close(); err != nil {
		t.Fatalf("root Close: %v", err)
	}
	var closed *transport.ClosedError
	if _, err := scoped.Get(ctx, "k"); !errors.As(err, &closed) {
		t.Fatalf("scoped Get after root Close: err = %v, want *transport.ClosedError", err)
	}
	err := scoped.SealStorePut(ctx, &transport.SealStorePutRequest{Name: "n", Secret: []byte("s")})
	if !errors.As(err, &closed) {
		t.Fatalf("scoped SealStorePut after root Close: err = %v, want *transport.ClosedError", err)
	}
	if _, err := scoped.SealStoreGet(ctx, &transport.SealStoreGetRequest{Name: "n"}); !errors.As(err, &closed) {
		t.Fatalf("scoped SealStoreGet after root Close: err = %v, want *transport.ClosedError", err)
	}
}

// TestTransport_ScopedCloseLeavesRootOpen pins that a scoped clone's Close
// closes only that clone.
func TestTransport_ScopedCloseLeavesRootOpen(t *testing.T) {
	root, scoped := newScopedPair(t)
	ctx := context.Background()
	if err := root.Put(ctx, "k", []byte("v")); err != nil {
		t.Fatalf("root Put: %v", err)
	}
	if err := scoped.Close(); err != nil {
		t.Fatalf("scoped Close: %v", err)
	}
	var closed *transport.ClosedError
	if _, err := scoped.Get(ctx, "k"); !errors.As(err, &closed) {
		t.Fatalf("scoped Get after its Close: err = %v, want *transport.ClosedError", err)
	}
	got, err := root.Get(ctx, "k")
	if err != nil {
		t.Fatalf("root Get after scoped Close: %v", err)
	}
	if string(got) != "v" {
		t.Fatalf("root Get = %q, want %q", got, "v")
	}
}

// TestLifecycle_ShutWaitsForAnAdmittedEnter pins that shut returns only after
// every call enter admitted has released, and that enter refuses afterwards.
func TestLifecycle_ShutWaitsForAnAdmittedEnter(t *testing.T) {
	l := &lifecycle{}
	release, err := l.enter()
	if err != nil {
		t.Fatalf("enter: %v", err)
	}
	done := make(chan struct{})
	go func() {
		l.shut()
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("shut returned while an admitted call was still running")
	default:
	}
	release()
	<-done
	var closed *transport.ClosedError
	if _, err := l.enter(); !errors.As(err, &closed) {
		t.Fatalf("enter after shut: err = %v, want *transport.ClosedError", err)
	}
}

func TestLifecycle_EnterAfterShutIsRefused(t *testing.T) {
	l := &lifecycle{}
	release, err := l.enter()
	if err != nil {
		t.Fatalf("enter: %v", err)
	}
	release()
	l.shut()
	release, err = l.enter()
	var closed *transport.ClosedError
	if !errors.As(err, &closed) {
		t.Fatalf("enter after shut: err = %v, want *transport.ClosedError", err)
	}
	if release != nil {
		release()
	}
}
