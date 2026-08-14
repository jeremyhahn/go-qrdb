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

package sdk

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// unsupportedSQLClient is returned by Client.SQL when the underlying
// transport does not implement transport.SQLProvider (or the client is
// closed). Its Query method always fails with a typed
// transport.NotImplementedError instead of the caller taking a nil-pointer
// panic on a nil transport.SQLClient.
type unsupportedSQLClient struct{}

// Query always returns a *transport.NotImplementedError.
func (unsupportedSQLClient) Query(ctx context.Context, database, query string) (*transport.SQLQueryResult, error) {
	return nil, &transport.NotImplementedError{Method: "SQL"}
}

// Compile-time interface check.
var _ transport.SQLClient = unsupportedSQLClient{}

// SQL returns a SQLClient for executing ad-hoc SQL queries against a named
// database. The transport must implement transport.SQLProvider; otherwise
// (or when the client is closed) the returned client is non-nil but every
// call to Query fails with a typed transport.NotImplementedError, following
// the same comma-ok nil-guard pattern as Audit while avoiding a nil-pointer
// footgun for callers who don't check for nil.
func (c *Client) SQL() transport.SQLClient {
	if c.closed.Load() {
		return unsupportedSQLClient{}
	}
	if p, ok := c.transport.(transport.SQLProvider); ok {
		return p.SQLClient()
	}
	return unsupportedSQLClient{}
}
