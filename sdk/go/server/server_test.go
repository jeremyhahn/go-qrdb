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

package server

import (
	"errors"
	"testing"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
)

// TestWithAuditLogger_ReturnsServerOption verifies the SDK re-export produces
// a non-nil ServerOption usable at node construction.
func TestWithAuditLogger_ReturnsServerOption(t *testing.T) {
	opt := WithAuditLogger(sdk.NewAuditRingBuffer(8))
	if opt == nil {
		t.Fatal("WithAuditLogger returned nil ServerOption")
	}
}

// TestWithAuditLogger_NilLoggerStillBuildsOption verifies a nil sink is
// accepted at option construction; the node decides what a nil sink means.
func TestWithAuditLogger_NilLoggerStillBuildsOption(t *testing.T) {
	if WithAuditLogger(nil) == nil {
		t.Fatal("WithAuditLogger(nil) returned nil ServerOption")
	}
}

// TestCertProviderConflictError_MatchesThroughErrorsAs verifies the alias is
// the lifecycle type: errors.As through the re-export finds a wrapped value.
func TestCertProviderConflictError_MatchesThroughErrorsAs(t *testing.T) {
	wrapped := errors.Join(errors.New("boot"), &CertProviderConflictError{})
	var conflict *CertProviderConflictError
	if !errors.As(wrapped, &conflict) {
		t.Fatalf("errors.As(%v) found no *CertProviderConflictError", wrapped)
	}
	if conflict.Error() == "" {
		t.Fatal("CertProviderConflictError has an empty message")
	}
}
