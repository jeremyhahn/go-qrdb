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
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// TestMapServiceError_TableLayoutUnsupportedIsFailedPrecondition pins the
// gRPC classification -- which the Unix socket and MCP transports reuse --
// of a table created by v0.20.0 through v0.21.0.
func TestMapServiceError_TableLayoutUnsupportedIsFailedPrecondition(t *testing.T) {
	layout := &service.TableLayoutUnsupportedError{Database: "shop", Name: "users", TableID: 7}
	for name, err := range map[string]error{
		"sql engine": &service.SQLExecutionError{Err: layoutSQLError{}},
		"bare":       layout,
		"read":       &service.ReadError{Err: layout},
	} {
		if got := status.Code(mapServiceError(err)); got != codes.FailedPrecondition {
			t.Errorf("%s: mapServiceError = %v, want FailedPrecondition", name, got)
		}
		if got := ServiceErrorCode(err); got != codes.FailedPrecondition {
			t.Errorf("%s: ServiceErrorCode = %v, want FailedPrecondition", name, got)
		}
	}
}

// layoutSQLError is the SQL engine's rendering of the layout refusal: a
// numbered error carrying TableLayoutUnsupportedSQLCode, as the MySQL engine
// shapes it.
type layoutSQLError struct{}

func (layoutSQLError) Error() string { return "table shop/users: drop the table and re-create it" }
func (layoutSQLError) Number() int   { return service.TableLayoutUnsupportedSQLCode }
