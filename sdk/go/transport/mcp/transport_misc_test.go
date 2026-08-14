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

package mcp

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// transport_watch.go - WatchPrefix on closed transport
// ---------------------------------------------------------------------------

func TestWatchPrefix_Closed(t *testing.T) {
	tr := newClosedTransport()
	var ce *transport.ClosedError

	if _, err := tr.WatchPrefix(context.Background(), "p", transport.WatchOptions{}); !errors.As(err, &ce) {
		t.Errorf("WatchPrefix: %v", err)
	}
}

func TestWatch_Closed(t *testing.T) {
	tr := newClosedTransport()
	var ce *transport.ClosedError

	if _, err := tr.Watch(context.Background(), "k", transport.WatchOptions{}); !errors.As(err, &ce) {
		t.Errorf("Watch: %v", err)
	}
}

// ---------------------------------------------------------------------------
// isWatchCanceledError
// ---------------------------------------------------------------------------

func TestIsWatchCanceledError(t *testing.T) {
	if isWatchCanceledError(nil) {
		t.Error("nil should return false")
	}
	if !isWatchCanceledError(errors.New("watch canceled by server")) {
		t.Error("'canceled' message should return true")
	}
	if isWatchCanceledError(errors.New("other error")) {
		t.Error("non-cancel error should return false")
	}
}
