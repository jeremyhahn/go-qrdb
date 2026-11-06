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

package embedded

import (
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestErrorMessages(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{&MissingAuditLoggerError{}, "embedded: audit logger is not configured"},
		{&UnregisteredOperationError{Method: "Frobnicate"}, "embedded: Frobnicate has no registered permission; refusing rather than admitting an uncontrolled operation"},
		{&NilRequestError{Method: "Get"}, "embedded: Get request must not be nil"},
		{&BarrierSealedError{}, "embedded: data-plane barrier is sealed"},
		{&BarrierSealedError{Err: errors.New("inner")}, "embedded: data-plane barrier is sealed: inner"},
	}
	for _, tt := range tests {
		if got := tt.err.Error(); got != tt.want {
			t.Errorf("%T.Error() = %q, want %q", tt.err, got, tt.want)
		}
	}
}

// TestTranslateError_VectorAndIndexErrors complements transport_test.go's
// TestTranslateError_* cases, which cover nil and the KV errors.
func TestTranslateError_VectorAndIndexErrors(t *testing.T) {
	is := func(target any) func(error) bool {
		return func(err error) bool { return errors.As(err, target) }
	}
	tests := []struct {
		in   error
		want func(error) bool
	}{
		{&service.VectorNotFoundError{Namespace: "ns1", ID: "v1"}, func(err error) bool {
			te, ok := errors.AsType[*transport.VectorNotFoundError](err)
			return ok && te.Namespace == "ns1" && te.ID == "v1"
		}},
		{&service.NamespaceNotFoundError{Namespace: "ns2"}, is(new(*transport.NamespaceNotFoundError))},
		{&service.NamespaceExistsError{Namespace: "dup"}, is(new(*transport.NamespaceExistsError))},
		{&service.DimensionMismatchError{Expected: 768, Actual: 512}, func(err error) bool {
			te, ok := errors.AsType[*transport.DimensionMismatchError](err)
			return ok && te.Expected == 768 && te.Got == 512
		}},
		{&service.IndexNotFoundError{Name: "idx1"}, is(new(*transport.IndexNotFoundError))},
		{&service.IndexExistsError{Name: "idx2"}, is(new(*transport.IndexExistsError))},
	}
	for _, tt := range tests {
		if got := translateError(tt.in); !tt.want(got) {
			t.Errorf("translateError(%T) = %T (%v)", tt.in, got, got)
		}
	}
}
