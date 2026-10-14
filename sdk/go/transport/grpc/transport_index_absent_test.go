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

package grpc

import (
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestMapIndexAbsent_NotFoundIsIndexNotFound pins that the server's NotFound
// on an index query or scan reaches the caller as the typed
// IndexNotFoundError, naming the index the caller asked for.
func TestMapIndexAbsent_NotFoundIsIndexNotFound(t *testing.T) {
	err := mapIndexAbsent("IndexQuery", "by_email",
		status.Error(codes.NotFound, "service: index not found: by_email"))
	var inf *transport.IndexNotFoundError
	if !errors.As(err, &inf) {
		t.Fatalf("expected *transport.IndexNotFoundError, got %T: %v", err, err)
	}
	if inf.Name != "by_email" {
		t.Fatalf("Name = %q, want %q", inf.Name, "by_email")
	}
}

// TestMapIndexAbsent_OtherCodesTakeTheGenericMapping pins that only NotFound
// is rewritten: an internal fault is not reported as a missing index.
func TestMapIndexAbsent_OtherCodesTakeTheGenericMapping(t *testing.T) {
	err := mapIndexAbsent("IndexScan", "by_email", status.Error(codes.Internal, "boom"))
	var inf *transport.IndexNotFoundError
	if errors.As(err, &inf) {
		t.Fatalf("an internal fault must not map to IndexNotFoundError: %v", err)
	}
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
}
