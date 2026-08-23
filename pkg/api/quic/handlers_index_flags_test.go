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
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/index"
)

// captureIndexCreateConfig drives handleIndexCreate with req and returns the
// index.IndexConfig the handler built from it.
func captureIndexCreateConfig(t *testing.T, req *pb.IndexCreateRequest) index.IndexConfig {
	t.Helper()
	var got index.IndexConfig
	indexSvc := &mockIndexService{
		createIndexFn: func(_ context.Context, _, _ string, config index.IndexConfig) error {
			got = config
			return nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithIndex(indexSvc))

	payload, err := proto.Marshal(req)
	if err != nil {
		t.Fatalf("marshal IndexCreateRequest: %v", err)
	}
	if _, err := srv.handleIndexCreate(context.Background(), payload); err != nil {
		t.Fatalf("handleIndexCreate: %v", err)
	}
	return got
}

// TestHandleIndexCreate_MapsTheNewFlags covers go-qrdb#34 on the QUIC plane.
// The handler read only Unique and Sparse, so a numeric or case-insensitive
// request built an ordinary case-SENSITIVE string index while reporting
// success — a lookup that answers empty with a nil error.
func TestHandleIndexCreate_MapsTheNewFlags(t *testing.T) {
	got := captureIndexCreateConfig(t, &pb.IndexCreateRequest{
		Name:            "by_email",
		FieldName:       "email",
		Unique:          true,
		Sparse:          true,
		Numeric:         true,
		CaseInsensitive: true,
	})

	if !got.Unique || !got.Sparse {
		t.Errorf("unique/sparse lost: %+v", got)
	}
	if got.ValueType != index.IndexValueTypeNumeric {
		t.Errorf("ValueType = %v; want numeric", got.ValueType)
	}
	if got.CaseSensitive {
		t.Error("a case-insensitive request produced CaseSensitive:true")
	}
}

// TestHandleIndexCreate_ZeroRequestIsTheDefaultIndex is the control and the
// polarity guard. index.IndexConfig names CaseSensitive for the DEFAULT, so
// its zero value folds case: a handler that left the field unset would turn
// every ordinary QUIC-created index case-insensitive.
func TestHandleIndexCreate_ZeroRequestIsTheDefaultIndex(t *testing.T) {
	got := captureIndexCreateConfig(t, &pb.IndexCreateRequest{
		Name:      "by_name",
		FieldName: "name",
	})

	if !got.CaseSensitive {
		t.Error("an unset case_insensitive produced a case-INSENSITIVE index; " +
			"absent must mean case-sensitive")
	}
	if got.Unique || got.Sparse || got.ValueType == index.IndexValueTypeNumeric {
		t.Errorf("an empty request produced %+v; want no claims", got)
	}
}
