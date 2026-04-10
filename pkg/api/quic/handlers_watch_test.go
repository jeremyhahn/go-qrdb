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

package quic

import (
	"context"
	"errors"
	"testing"
)

// ---------------------------------------------------------------------------
// handleWatchSetup
// ---------------------------------------------------------------------------

func TestHandleWatchSetup_ReturnsServiceNotConfigured(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	ctx := context.Background()

	_, err := srv.handleWatchSetup(ctx, nil)
	if err == nil {
		t.Fatal("expected error from handleWatchSetup, got nil")
	}

	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T: %v", err, err)
	}
	if snc.Service != "watch" {
		t.Errorf("expected service %q, got %q", "watch", snc.Service)
	}
}

func TestHandleWatchSetup_WithWatchService(t *testing.T) {
	// Even when a watch service is configured, the setup handler still
	// returns ServiceNotConfiguredError because watches use the streaming
	// path, not the request-response path.
	watchSvc := &mockWatchService{}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithWatch(watchSvc))
	ctx := context.Background()

	_, err := srv.handleWatchSetup(ctx, nil)
	if err == nil {
		t.Fatal("expected error from handleWatchSetup even with watch service, got nil")
	}

	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleWatchPrefixSetup
// ---------------------------------------------------------------------------

func TestHandleWatchPrefixSetup_ReturnsServiceNotConfigured(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	ctx := context.Background()

	_, err := srv.handleWatchPrefixSetup(ctx, nil)
	if err == nil {
		t.Fatal("expected error from handleWatchPrefixSetup, got nil")
	}

	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T: %v", err, err)
	}
	if snc.Service != "watch" {
		t.Errorf("expected service %q, got %q", "watch", snc.Service)
	}
}

func TestHandleWatchPrefixSetup_WithWatchService(t *testing.T) {
	// Even when a watch service is configured, the prefix setup handler
	// still returns ServiceNotConfiguredError because prefix watches use
	// the streaming path, not the request-response path.
	watchSvc := &mockWatchService{}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithWatch(watchSvc))
	ctx := context.Background()

	_, err := srv.handleWatchPrefixSetup(ctx, nil)
	if err == nil {
		t.Fatal("expected error from handleWatchPrefixSetup even with watch service, got nil")
	}

	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T: %v", err, err)
	}
}
