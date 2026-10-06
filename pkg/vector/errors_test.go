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

package vector

import (
	"errors"
	"strings"
	"testing"
)

// Every typed error's Error() must mention the identifying detail it
// carries, so a log line or a client message is actionable without a
// debugger.
func TestErrors_MessagesNameTheirDetail(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want []string
	}{
		{"InvalidRecordError", &InvalidRecordError{Record: "vector", Reason: "empty"}, []string{"vector", "empty"}},
		{"UnsupportedRecordVersionError", &UnsupportedRecordVersionError{Record: "namespace", Version: 9}, []string{"namespace", "9"}},
		{"NamespaceExistsError", &NamespaceExistsError{Namespace: "docs"}, []string{"docs"}},
		{"NamespaceNotFoundError", &NamespaceNotFoundError{Namespace: "docs"}, []string{"docs"}},
		{"InvalidNamespaceNameError", &InvalidNamespaceNameError{Namespace: "a/b"}, []string{"a/b"}},
		{"InvalidDistanceMetricError", &InvalidDistanceMetricError{Namespace: "docs", Metric: 7}, []string{"docs", "7"}},
		{"InvalidIndexTypeError", &InvalidIndexTypeError{Namespace: "docs", IndexType: 9}, []string{"docs", "9"}},
		{"DimensionMismatchError", &DimensionMismatchError{Namespace: "docs", Got: 3, Want: 4}, []string{"docs", "3", "4"}},
		{"VectorNotFoundError", &VectorNotFoundError{Namespace: "docs", ID: 99}, []string{"docs", "99"}},
		{"InvalidDimensionError", &InvalidDimensionError{Namespace: "docs"}, []string{"docs"}},
		{"ForgedKeyError", &ForgedKeyError{Key: []byte("__vec:ns/docs")}, []string{"__vec:ns/docs", KeyPrefix}},
		{"IDOverflowError", &IDOverflowError{Namespace: "docs"}, []string{"docs", "MaxUint64"}},
		{"DuplicateIDError", &DuplicateIDError{Namespace: "docs", ID: 5}, []string{"docs", "5"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg := tc.err.Error()
			if msg == "" {
				t.Fatal("Error() returned an empty string")
			}
			for _, want := range tc.want {
				if !strings.Contains(msg, want) {
					t.Errorf("message %q does not mention %q", msg, want)
				}
			}
		})
	}
}

// Every typed error above must implement the standard error interface with a
// pointer receiver, matching the typed-errors convention the whole package
// follows.
func TestErrors_ImplementErrorInterface(t *testing.T) {
	var errs = []error{
		&InvalidRecordError{},
		&UnsupportedRecordVersionError{},
		&NamespaceExistsError{},
		&NamespaceNotFoundError{},
		&InvalidNamespaceNameError{},
		&InvalidDistanceMetricError{},
		&InvalidIndexTypeError{},
		&InvalidDimensionError{},
		&DimensionMismatchError{},
		&VectorNotFoundError{},
		&ForgedKeyError{},
		&IDOverflowError{},
		&DuplicateIDError{},
	}
	for _, err := range errs {
		if err.Error() == "" {
			t.Errorf("%T.Error() returned an empty string on the zero value", err)
		}
	}
}

// Every refusal declared by this package (the protocol.RefusalError
// implementations pkg/engine's apply handlers return as plain Go errors, see
// the compile-time assertions below Refusal's declarations) must report a
// non-empty, stable label: statemachine.ResolveApplyError logs it, and an
// empty label would make a refusal indistinguishable from one the classifier
// could not identify.
func TestErrors_RefusalLabelsAreNonEmpty(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  interface{ Refusal() string }
	}{
		{"NamespaceExistsError", &NamespaceExistsError{}},
		{"NamespaceNotFoundError", &NamespaceNotFoundError{}},
		{"InvalidNamespaceNameError", &InvalidNamespaceNameError{}},
		{"InvalidDistanceMetricError", &InvalidDistanceMetricError{}},
		{"InvalidIndexTypeError", &InvalidIndexTypeError{}},
		{"InvalidDimensionError", &InvalidDimensionError{}},
		{"DimensionMismatchError", &DimensionMismatchError{}},
		{"VectorNotFoundError", &VectorNotFoundError{}},
		{"ForgedKeyError", &ForgedKeyError{}},
		{"IDOverflowError", &IDOverflowError{}},
		{"DuplicateIDError", &DuplicateIDError{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.err.Refusal() == "" {
				t.Errorf("%T.Refusal() returned an empty string", tc.err)
			}
		})
	}
}

func TestIsForgedKey(t *testing.T) {
	for _, tc := range []struct {
		key  string
		want bool
	}{
		{"__vec:ns/docs", true},
		{"__vec:v/docs/0000000000000001", true},
		{KeyPrefix, true},
		{"", false},
		{"__ve", false},
		{"other-key", false},
		{"__sys:roles/admin", false},
	} {
		if got := IsForgedKey([]byte(tc.key)); got != tc.want {
			t.Errorf("IsForgedKey(%q) = %v, want %v", tc.key, got, tc.want)
		}
	}
}

func TestRefuseForgedKeys(t *testing.T) {
	if err := RefuseForgedKeys([]byte("ordinary-key"), []byte("another-key")); err != nil {
		t.Fatalf("RefuseForgedKeys(ordinary keys) = %v, want nil", err)
	}

	err := RefuseForgedKeys([]byte("ordinary-key"), []byte("__vec:ns/docs"), []byte("never-reached"))
	if err == nil {
		t.Fatal("RefuseForgedKeys did not refuse a forged key")
	}
	var forged *ForgedKeyError
	if !errors.As(err, &forged) {
		t.Fatalf("error = %T (%v), want *ForgedKeyError", err, err)
	}
	if string(forged.Key) != "__vec:ns/docs" {
		t.Errorf("ForgedKeyError.Key = %q, want %q", forged.Key, "__vec:ns/docs")
	}
}
