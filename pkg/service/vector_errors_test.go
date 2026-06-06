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

package service

import (
	"errors"
	"testing"
)

func TestVectorNotFoundError(t *testing.T) {
	err := &VectorNotFoundError{Namespace: "embeddings", ID: "vec-42"}
	expected := "service: vector not found: embeddings/vec-42"
	if err.Error() != expected {
		t.Errorf("Error() = %q, want %q", err.Error(), expected)
	}
}

func TestVectorNotFoundErrorEmptyFields(t *testing.T) {
	err := &VectorNotFoundError{}
	expected := "service: vector not found: /"
	if err.Error() != expected {
		t.Errorf("Error() = %q, want %q", err.Error(), expected)
	}
}

func TestVectorNotFoundErrorImplementsError(t *testing.T) {
	var err error = &VectorNotFoundError{Namespace: "ns", ID: "id"}
	var target *VectorNotFoundError
	if !errors.As(err, &target) {
		t.Error("errors.As failed for VectorNotFoundError")
	}
	if target.Namespace != "ns" {
		t.Errorf("Namespace = %q, want %q", target.Namespace, "ns")
	}
	if target.ID != "id" {
		t.Errorf("ID = %q, want %q", target.ID, "id")
	}
}

func TestNamespaceNotFoundError(t *testing.T) {
	err := &NamespaceNotFoundError{Namespace: "documents"}
	expected := "service: namespace not found: documents"
	if err.Error() != expected {
		t.Errorf("Error() = %q, want %q", err.Error(), expected)
	}
}

func TestNamespaceNotFoundErrorEmptyNamespace(t *testing.T) {
	err := &NamespaceNotFoundError{}
	expected := "service: namespace not found: "
	if err.Error() != expected {
		t.Errorf("Error() = %q, want %q", err.Error(), expected)
	}
}

func TestNamespaceNotFoundErrorImplementsError(t *testing.T) {
	var err error = &NamespaceNotFoundError{Namespace: "test-ns"}
	var target *NamespaceNotFoundError
	if !errors.As(err, &target) {
		t.Error("errors.As failed for NamespaceNotFoundError")
	}
	if target.Namespace != "test-ns" {
		t.Errorf("Namespace = %q, want %q", target.Namespace, "test-ns")
	}
}

func TestNamespaceExistsError(t *testing.T) {
	err := &NamespaceExistsError{Namespace: "images"}
	expected := "service: namespace already exists: images"
	if err.Error() != expected {
		t.Errorf("Error() = %q, want %q", err.Error(), expected)
	}
}

func TestNamespaceExistsErrorEmptyNamespace(t *testing.T) {
	err := &NamespaceExistsError{}
	expected := "service: namespace already exists: "
	if err.Error() != expected {
		t.Errorf("Error() = %q, want %q", err.Error(), expected)
	}
}

func TestNamespaceExistsErrorImplementsError(t *testing.T) {
	var err error = &NamespaceExistsError{Namespace: "dup-ns"}
	var target *NamespaceExistsError
	if !errors.As(err, &target) {
		t.Error("errors.As failed for NamespaceExistsError")
	}
	if target.Namespace != "dup-ns" {
		t.Errorf("Namespace = %q, want %q", target.Namespace, "dup-ns")
	}
}

func TestDimensionMismatchError(t *testing.T) {
	err := &DimensionMismatchError{Expected: 384, Actual: 768}
	expected := "service: dimension mismatch: expected 384, got 768"
	if err.Error() != expected {
		t.Errorf("Error() = %q, want %q", err.Error(), expected)
	}
}

func TestDimensionMismatchErrorZeroDimensions(t *testing.T) {
	err := &DimensionMismatchError{Expected: 0, Actual: 0}
	expected := "service: dimension mismatch: expected 0, got 0"
	if err.Error() != expected {
		t.Errorf("Error() = %q, want %q", err.Error(), expected)
	}
}

func TestDimensionMismatchErrorImplementsError(t *testing.T) {
	var err error = &DimensionMismatchError{Expected: 128, Actual: 256}
	var target *DimensionMismatchError
	if !errors.As(err, &target) {
		t.Error("errors.As failed for DimensionMismatchError")
	}
	if target.Expected != 128 {
		t.Errorf("Expected = %d, want 128", target.Expected)
	}
	if target.Actual != 256 {
		t.Errorf("Actual = %d, want 256", target.Actual)
	}
}

func TestInvalidVectorError(t *testing.T) {
	err := &InvalidVectorError{Reason: "contains NaN values"}
	expected := "service: invalid vector: contains NaN values"
	if err.Error() != expected {
		t.Errorf("Error() = %q, want %q", err.Error(), expected)
	}
}

func TestInvalidVectorErrorEmptyReason(t *testing.T) {
	err := &InvalidVectorError{}
	expected := "service: invalid vector: "
	if err.Error() != expected {
		t.Errorf("Error() = %q, want %q", err.Error(), expected)
	}
}

func TestInvalidVectorErrorImplementsError(t *testing.T) {
	var err error = &InvalidVectorError{Reason: "empty vector"}
	var target *InvalidVectorError
	if !errors.As(err, &target) {
		t.Error("errors.As failed for InvalidVectorError")
	}
	if target.Reason != "empty vector" {
		t.Errorf("Reason = %q, want %q", target.Reason, "empty vector")
	}
}

func TestInvalidNamespaceNameError(t *testing.T) {
	err := &InvalidNamespaceNameError{Name: "bad\x1fname", Reason: "contains illegal control character 0x1f"}
	got := err.Error()
	if got == "" {
		t.Error("Error() must return non-empty string")
	}
}

func TestInvalidNamespaceNameErrorImplementsError(t *testing.T) {
	var err error = &InvalidNamespaceNameError{Name: "x", Reason: "too short"}
	var target *InvalidNamespaceNameError
	if !errors.As(err, &target) {
		t.Error("errors.As failed for InvalidNamespaceNameError")
	}
	if target.Name != "x" {
		t.Errorf("Name = %q, want %q", target.Name, "x")
	}
}

// ---------------------------------------------------------------------------
// validateNamespaceName tests
// ---------------------------------------------------------------------------

func TestValidateNamespaceName_ValidNames(t *testing.T) {
	valid := []string{
		"docs",
		"my-vectors",
		"namespace_with_underscore",
		"ns123",
		"a",
		"UPPERCASE",
		"with spaces", // spaces (0x20) are allowed
		"unicode-αβγ", // multi-byte UTF-8 is fine
	}
	for _, name := range valid {
		t.Run(name, func(t *testing.T) {
			if err := validateNamespaceName(name); err != nil {
				t.Errorf("validateNamespaceName(%q) = %v, want nil", name, err)
			}
		})
	}
}

func TestValidateNamespaceName_RejectsEmpty(t *testing.T) {
	err := validateNamespaceName("")
	if err == nil {
		t.Fatal("expected error for empty name, got nil")
	}
	var target *InvalidNamespaceNameError
	if !errors.As(err, &target) {
		t.Fatalf("expected InvalidNamespaceNameError, got %T: %v", err, err)
	}
}

func TestValidateNamespaceName_RejectsControlBytes(t *testing.T) {
	controls := []struct {
		name string
		b    byte
	}{
		{"null byte", 0x00},
		{"unit separator (namespaceSep)", 0x1f},
		{"tab", 0x09},
		{"newline", 0x0a},
		{"carriage return", 0x0d},
		{"escape", 0x1b},
	}
	for _, tc := range controls {
		t.Run(tc.name, func(t *testing.T) {
			name := "valid" + string([]byte{tc.b}) + "suffix"
			err := validateNamespaceName(name)
			if err == nil {
				t.Fatalf("validateNamespaceName(%q) = nil, want error for control byte 0x%02x", name, tc.b)
			}
			var target *InvalidNamespaceNameError
			if !errors.As(err, &target) {
				t.Fatalf("expected InvalidNamespaceNameError, got %T: %v", err, err)
			}
		})
	}
}

func TestValidateNamespaceName_RejectsTooLong(t *testing.T) {
	name := ""
	for i := 0; i <= maxNamespaceNameLen; i++ {
		name += "a"
	}
	err := validateNamespaceName(name)
	if err == nil {
		t.Fatal("validateNamespaceName: expected error for name exceeding max length, got nil")
	}
	var target *InvalidNamespaceNameError
	if !errors.As(err, &target) {
		t.Fatalf("expected InvalidNamespaceNameError, got %T: %v", err, err)
	}
}

func TestValidateNamespaceName_MaxLengthExactlyAccepted(t *testing.T) {
	name := ""
	for i := 0; i < maxNamespaceNameLen; i++ {
		name += "a"
	}
	if err := validateNamespaceName(name); err != nil {
		t.Errorf("validateNamespaceName: expected nil for exactly-max-length name, got %v", err)
	}
}
