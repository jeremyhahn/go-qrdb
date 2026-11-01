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

package transport

import (
	"errors"
	"maps"
	"testing"
)

func TestNamespaceNotFoundError_MetadataRoundTrip(t *testing.T) {
	for _, ns := range []string{"embeddings", "", "a/b c"} {
		want := &NamespaceNotFoundError{Namespace: ns}
		md := want.Metadata()
		if !maps.Equal(md, map[string]string{"namespace": ns}) {
			t.Fatalf("Metadata() = %v, want namespace=%q", md, ns)
		}
		if got := NamespaceNotFoundFromMetadata(md); *got != *want {
			t.Fatalf("NamespaceNotFoundFromMetadata() = %+v, want %+v", got, want)
		}
	}
}

func TestNamespaceNotFoundFromMetadata_absentKeyKeepsType(t *testing.T) {
	for name, md := range map[string]map[string]string{
		"nil":   nil,
		"empty": {},
		"other": {"expected": "3"},
	} {
		t.Run(name, func(t *testing.T) {
			got := NamespaceNotFoundFromMetadata(md)
			if got == nil || got.Namespace != "" {
				t.Fatalf("NamespaceNotFoundFromMetadata(%v) = %+v, want an empty namespace", md, got)
			}
		})
	}
}

func TestDimensionMismatchError_MetadataRoundTrip(t *testing.T) {
	for _, want := range []*DimensionMismatchError{
		{Expected: 768, Got: 384},
		{Expected: 0, Got: 0},
		{Expected: -1, Got: 1 << 30},
	} {
		md := want.Metadata()
		if len(md) != 2 {
			t.Fatalf("Metadata() = %v, want expected and got", md)
		}
		if got := DimensionMismatchFromMetadata(md); *got != *want {
			t.Fatalf("DimensionMismatchFromMetadata(%v) = %+v, want %+v", md, got, want)
		}
	}
}

func TestDimensionMismatchFromMetadata_malformedNumbersAreZero(t *testing.T) {
	tests := []struct {
		name string
		md   map[string]string
		want DimensionMismatchError
	}{
		{name: "nil", md: nil},
		{name: "empty", md: map[string]string{}},
		{name: "expected only", md: map[string]string{"expected": "8"}, want: DimensionMismatchError{Expected: 8}},
		{name: "got only", md: map[string]string{"got": "4"}, want: DimensionMismatchError{Got: 4}},
		{name: "not numbers", md: map[string]string{"expected": "eight", "got": "4x"}},
		{name: "one malformed", md: map[string]string{"expected": "eight", "got": "4"}, want: DimensionMismatchError{Got: 4}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DimensionMismatchFromMetadata(tt.md)
			if got == nil || *got != tt.want {
				t.Fatalf("DimensionMismatchFromMetadata(%v) = %+v, want %+v", tt.md, got, tt.want)
			}
		})
	}
}

func TestRefusalError_vectorNamespaceNotFound(t *testing.T) {
	want := &NamespaceNotFoundError{Namespace: "docs"}
	err := RefusalError(ReasonVectorNamespaceNotFound, want.Metadata())
	got, ok := errors.AsType[*NamespaceNotFoundError](err)
	if !ok || *got != *want {
		t.Fatalf("RefusalError() = %v, want %+v", err, want)
	}
}

func TestRefusalError_vectorDimensionMismatch(t *testing.T) {
	want := &DimensionMismatchError{Expected: 3, Got: 5}
	err := RefusalError(ReasonVectorDimensionMismatch, want.Metadata())
	got, ok := errors.AsType[*DimensionMismatchError](err)
	if !ok || *got != *want {
		t.Fatalf("RefusalError() = %v, want %+v", err, want)
	}
	if err.Error() != "transport: dimension mismatch: expected 3, got 5" {
		t.Fatalf("Error() = %q", err.Error())
	}
}

func TestRefusalError_vectorMalformedMetadataKeepsClassification(t *testing.T) {
	if _, ok := errors.AsType[*NamespaceNotFoundError](RefusalError(ReasonVectorNamespaceNotFound, nil)); !ok {
		t.Fatal("a namespace refusal without metadata lost its type")
	}
	if _, ok := errors.AsType[*DimensionMismatchError](RefusalError(ReasonVectorDimensionMismatch, map[string]string{"expected": "x"})); !ok {
		t.Fatal("a dimension refusal with malformed metadata lost its type")
	}
}

func TestErrorMessages_uncoveredTypes(t *testing.T) {
	cause := errors.New("bad key")
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"scope unsupported", &DatabaseScopeUnsupportedError{Database: "db1", Reason: "no header"}, "transport: cannot scope to database db1: no header"},
		{"decryption bare", &BackupDecryptionFailedError{}, "transport: backup decryption failed"},
		{"decryption full", &BackupDecryptionFailedError{BackupID: "b1", Err: cause}, "transport: backup decryption failed for b1: bad key"},
		{"plaintext restore", &BackupPlaintextRestoreNotAllowedError{BackupID: "b2"}, "transport: backup b2 is stored as plaintext; restoring it requires an explicit opt-in (allow_plaintext)"},
		{"nil request", &NilRequestError{}, "transport: nil request"},
		{"unexpected status", &UnexpectedStatusError{StatusCode: 502, Body: "bad gateway"}, "transport: unexpected status 502: bad gateway"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.want {
				t.Fatalf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBackupDecryptionFailedError_Unwrap(t *testing.T) {
	cause := errors.New("bad key")
	if err := error(&BackupDecryptionFailedError{Err: cause}); !errors.Is(err, cause) {
		t.Fatalf("errors.Is(%v, cause) = false", err)
	}
	if got := (&BackupDecryptionFailedError{}).Unwrap(); got != nil {
		t.Fatalf("Unwrap() = %v, want nil", got)
	}
}
