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
