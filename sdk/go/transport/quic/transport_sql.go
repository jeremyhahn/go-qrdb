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

package quic

import (
	"context"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertions.
var (
	_ transport.SQLClient   = (*Transport)(nil)
	_ transport.SQLProvider = (*Transport)(nil)
)

// Query executes an ad-hoc SQL query against a named database via the QUIC
// sql.Query handler.
func (t *Transport) Query(ctx context.Context, database, query string) (*transport.SQLQueryResult, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := proto.Marshal(&pb.SQLQueryRequest{
		Query:    query,
		Database: database,
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "Query", Err: err}
	}

	respBytes, err := t.call(ctx, "sql.Query", payload)
	if err != nil {
		return nil, &transport.RequestError{Method: "Query", Err: err}
	}

	var resp pb.SQLQueryResponse
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &transport.RequestError{Method: "Query", Err: err}
	}

	return quicProtoToSQLQueryResult(&resp), nil
}

// SQLClient returns the QUIC transport itself, which implements
// transport.SQLClient directly.
func (t *Transport) SQLClient() transport.SQLClient {
	return t
}

// quicProtoToSQLQueryResult converts a SQLQueryResponse proto to a
// transport SQLQueryResult.
func quicProtoToSQLQueryResult(resp *pb.SQLQueryResponse) *transport.SQLQueryResult {
	rows := make([]map[string]any, len(resp.GetRows()))
	for i, row := range resp.GetRows() {
		values := make(map[string]any, len(row.GetValues()))
		for k, v := range row.GetValues() {
			values[k] = v
		}
		rows[i] = values
	}

	return &transport.SQLQueryResult{
		Columns:  resp.GetColumns(),
		Rows:     rows,
		RowCount: int(resp.GetRowCount()),
		Duration: resp.GetDuration(),
	}
}
