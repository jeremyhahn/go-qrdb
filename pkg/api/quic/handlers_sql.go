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
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// handleSQLQuery executes an ad-hoc SQL query against a named database by
// delegating to the SQL service layer. Mirrors the REST POST /sql/query and
// gRPC SQLQuery handlers.
func (s *Server) handleSQLQuery(ctx context.Context, payload []byte) ([]byte, error) {
	if s.sqlSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "sql"}
	}

	var req pb.SQLQueryRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	if req.GetQuery() == "" {
		return nil, &service.SQLEmptyQueryError{}
	}
	if req.GetDatabase() == "" {
		return nil, &service.SQLEmptyDatabaseError{}
	}

	result, err := s.sqlSvc.Execute(ctx, req.GetDatabase(), req.GetQuery())
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(sqlQueryResultToProto(result))
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// sqlQueryResultToProto converts a service.SQLQueryResult to its protobuf
// representation. Every cell in service.SQLQueryResult.Rows is already a
// pre-formatted string (see the SQLQueryResult doc comment), so each row
// converts directly to a map[string]string-backed SQLRow.
func sqlQueryResultToProto(r *service.SQLQueryResult) *pb.SQLQueryResponse {
	if r == nil {
		return &pb.SQLQueryResponse{}
	}

	rows := make([]*pb.SQLRow, len(r.Rows))
	for i, row := range r.Rows {
		values := make(map[string]string, len(row))
		for k, v := range row {
			if s, ok := v.(string); ok {
				values[k] = s
			}
		}
		rows[i] = &pb.SQLRow{Values: values}
	}

	return &pb.SQLQueryResponse{
		Columns:  r.Columns,
		Rows:     rows,
		RowCount: int64(r.RowCount),
		Duration: r.Duration,
	}
}
