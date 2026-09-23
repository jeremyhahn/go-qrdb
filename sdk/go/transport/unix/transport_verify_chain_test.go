// Copyright 2026 Jeremy Hahn
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

package unix

import (
	"context"
	"errors"
	"testing"

	unixserver "github.com/jeremyhahn/go-qrdb/pkg/api/unix"
	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// fakeVerifyingAuditLogger is a minimal audit.AuditLogger + audit.Verifier
// for round-trip VerifyChain tests.
type fakeVerifyingAuditLogger struct {
	verifyFn func(ctx context.Context) (*audit.ChainVerification, error)
}

func (f *fakeVerifyingAuditLogger) Emit(_ context.Context, _ audit.AuditEvent) error { return nil }
func (f *fakeVerifyingAuditLogger) Query(_ context.Context, _ audit.Filter) (*audit.Page, error) {
	return &audit.Page{}, nil
}
func (f *fakeVerifyingAuditLogger) Close() error { return nil }
func (f *fakeVerifyingAuditLogger) VerifyChainSet(ctx context.Context) (*audit.ChainVerification, error) {
	return f.verifyFn(ctx)
}

var (
	_ audit.AuditLogger = (*fakeVerifyingAuditLogger)(nil)
	_ audit.Verifier    = (*fakeVerifyingAuditLogger)(nil)
)

func TestUnixRT_VerifyChain(t *testing.T) {
	logger := &fakeVerifyingAuditLogger{
		verifyFn: func(_ context.Context) (*audit.ChainVerification, error) {
			return &audit.ChainVerification{Files: 2, Records: 10, Genesis: true}, nil
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithAuditLogger(logger))
	defer cleanup()

	result, err := tr.VerifyChain(context.Background())
	if err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
	if !result.Verified || result.Files != 2 || result.Records != 10 {
		t.Fatalf("result = %+v", result)
	}
}

// TestUnixRT_VerifyChain_ChainBroken asserts a tamper verdict decodes as a
// successful result, not an error.
func TestUnixRT_VerifyChain_ChainBroken(t *testing.T) {
	logger := &fakeVerifyingAuditLogger{
		verifyFn: func(_ context.Context) (*audit.ChainVerification, error) {
			return &audit.ChainVerification{Files: 1, Records: 3},
				&audit.ChainBrokenError{Path: "audit-events.ndjson", Index: 1, Expected: "aa", Got: "bb"}
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithAuditLogger(logger))
	defer cleanup()

	result, err := tr.VerifyChain(context.Background())
	if err != nil {
		t.Fatalf("expected a successful result carrying the verdict, got error: %v", err)
	}
	if result.Verified {
		t.Error("expected Verified=false")
	}
}

func TestUnixRT_VerifyChain_NotImplemented(t *testing.T) {
	// No audit logger registered at all.
	tr, cleanup := startAuthServer(t)
	defer cleanup()

	_, err := tr.VerifyChain(context.Background())
	var nie *transport.NotImplementedError
	if !errors.As(err, &nie) {
		t.Fatalf("expected *transport.NotImplementedError, got %v", err)
	}
}

func TestUnixRT_VerifyChain_Closed(t *testing.T) {
	tr := &Transport{}
	tr.closed.Store(true)
	_, err := tr.VerifyChain(context.Background())
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected *transport.ClosedError, got %v", err)
	}
}
