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
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestMapIndexAbsent_WireNotFoundIsIndexNotFound pins that the server's wire
// not-found on an index query or scan reaches the caller as the typed
// IndexNotFoundError, naming the index the caller asked for.
func TestMapIndexAbsent_WireNotFoundIsIndexNotFound(t *testing.T) {
	err := mapIndexAbsent("by_email", &transport.KeyNotFoundError{Key: "service: index not found: by_email"})
	var inf *transport.IndexNotFoundError
	if !errors.As(err, &inf) {
		t.Fatalf("expected *transport.IndexNotFoundError, got %T: %v", err, err)
	}
	if inf.Name != "by_email" {
		t.Fatalf("Name = %q, want %q", inf.Name, "by_email")
	}
}

// TestMapIndexAbsent_OtherErrorsPassThrough pins that only a not-found is
// rewritten.
func TestMapIndexAbsent_OtherErrorsPassThrough(t *testing.T) {
	other := &transport.RequestError{Method: "index.Query", Err: errors.New("boom")}
	if got := mapIndexAbsent("by_email", other); got != other {
		t.Fatalf("mapIndexAbsent changed an unrelated error: %v", got)
	}
	if got := mapIndexAbsent("by_email", nil); got != nil {
		t.Fatalf("mapIndexAbsent(nil) = %v, want nil", got)
	}
}
