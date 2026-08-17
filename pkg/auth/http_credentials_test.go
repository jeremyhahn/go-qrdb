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

package auth

import (
	"context"
	"testing"
)

// TestHTTPAuthorization_RoundTrip verifies the Authorization carrier stores
// and retrieves the raw header value unchanged.
func TestHTTPAuthorization_RoundTrip(t *testing.T) {
	ctx := WithHTTPAuthorization(context.Background(), "Bearer abc.def.ghi")
	if got := HTTPAuthorizationFromContext(ctx); got != "Bearer abc.def.ghi" {
		t.Fatalf("HTTPAuthorizationFromContext = %q, want %q", got, "Bearer abc.def.ghi")
	}
}

// TestHTTPAuthorization_EmptyNotStored verifies an empty header value is not
// stored (WithHTTPAuthorization returns the context unchanged) and an
// unpopulated context reads back empty.
func TestHTTPAuthorization_EmptyNotStored(t *testing.T) {
	base := context.Background()
	ctx := WithHTTPAuthorization(base, "")
	if ctx != base {
		t.Fatal("empty Authorization must not allocate a new context")
	}
	if got := HTTPAuthorizationFromContext(ctx); got != "" {
		t.Fatalf("HTTPAuthorizationFromContext on empty context = %q, want empty", got)
	}
}
