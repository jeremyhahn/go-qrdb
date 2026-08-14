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
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// SQLQuery executes an ad-hoc SQL query against a named database and
// returns the result set. Mirrors the REST POST /sql/query handler
// (handleSQLQuery): the database field is canonical, empty query/database
// are rejected, and the response shape is the same
// service.SQLQueryResult wire contract, just carried over protobuf instead
// of JSON.
func (s *qrdbServiceServer) SQLQuery(ctx context.Context, req *pb.SQLQueryRequest) (*pb.SQLQueryResponse, error) {
	if s.sqlSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "sql"}).Error())
	}

	if req.GetQuery() == "" {
		return nil, status.Error(codes.InvalidArgument, (&service.SQLEmptyQueryError{}).Error())
	}
	if req.GetDatabase() == "" {
		return nil, status.Error(codes.InvalidArgument, (&service.SQLEmptyDatabaseError{}).Error())
	}

	result, err := s.sqlSvc.Execute(ctx, req.GetDatabase(), req.GetQuery())
	if err != nil {
		return nil, mapServiceError(err)
	}

	return sqlQueryResultToProto(result), nil
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
