// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package remote

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"google.golang.org/grpc"

	proto "github.com/jeremyhahn/go-qrdb/pkg/vector/gpu/remote/proto"
)

// fakeConn is a test double for grpc.ClientConnInterface. It records the
// last method invoked and can be configured to return a response payload
// or an error.
type fakeConn struct {
	// wantErr causes Invoke to return an error.
	wantErr error
	// respPayload is the JSON payload written into the resp pointer on success.
	// When empty and wantErr is nil, Invoke writes nothing (simulates an empty body).
	respPayload []byte
	// lastMethod records the full method name of the most recent Invoke call.
	lastMethod string
}

func (f *fakeConn) Invoke(_ context.Context, method string, _ any, resp any, _ ...grpc.CallOption) error {
	f.lastMethod = method
	if f.wantErr != nil {
		return f.wantErr
	}
	if len(f.respPayload) == 0 {
		return nil
	}
	// resp is *[]byte in the hand-written client's invoke helper.
	if dst, ok := resp.(*[]byte); ok {
		*dst = f.respPayload
	}
	return nil
}

func (f *fakeConn) NewStream(_ context.Context, _ *grpc.StreamDesc, _ string, _ ...grpc.CallOption) (grpc.ClientStream, error) {
	return nil, errors.New("streaming not supported")
}

// newClientConn returns a vectorIndexServiceClient backed by fakeConn.
func newClientConn(conn *fakeConn) proto.VectorIndexServiceClient {
	return newVectorIndexServiceClient(conn)
}

// jsonOf marshals v to JSON, panicking if it fails (test helper only).
func jsonOf(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// ---------------------------------------------------------------------------
// newVectorIndexServiceClient
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Insert
// ---------------------------------------------------------------------------

func TestVectorIndexServiceClient_Insert_Success(t *testing.T) {
	conn := &fakeConn{respPayload: jsonOf(&proto.InsertResponse{})}
	client := newClientConn(conn)

	resp, err := client.Insert(context.Background(), &proto.InsertRequest{ID: 1, Vector: []float32{0.1}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if conn.lastMethod != proto.VectorIndexService_Insert_FullMethodName {
		t.Errorf("unexpected method: %s", conn.lastMethod)
	}
}

func TestVectorIndexServiceClient_Insert_RPCError(t *testing.T) {
	sentinel := errors.New("rpc insert error")
	conn := &fakeConn{wantErr: sentinel}
	client := newClientConn(conn)

	_, err := client.Insert(context.Background(), &proto.InsertRequest{ID: 2})
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Delete
// ---------------------------------------------------------------------------

func TestVectorIndexServiceClient_Delete_Success(t *testing.T) {
	conn := &fakeConn{respPayload: jsonOf(&proto.DeleteResponse{})}
	client := newClientConn(conn)

	resp, err := client.Delete(context.Background(), &proto.DeleteRequest{ID: 7})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if conn.lastMethod != proto.VectorIndexService_Delete_FullMethodName {
		t.Errorf("unexpected method: %s", conn.lastMethod)
	}
}

func TestVectorIndexServiceClient_Delete_RPCError(t *testing.T) {
	sentinel := errors.New("rpc delete error")
	conn := &fakeConn{wantErr: sentinel}
	_, err := newClientConn(conn).Delete(context.Background(), &proto.DeleteRequest{ID: 1})
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Search
// ---------------------------------------------------------------------------

func TestVectorIndexServiceClient_Search_Success(t *testing.T) {
	payload := jsonOf(&proto.SearchResponse{
		Results: []*proto.SearchResult{
			{ID: 10, Distance: 0.5},
			{ID: 20, Distance: 1.2},
		},
	})
	conn := &fakeConn{respPayload: payload}
	client := newClientConn(conn)

	resp, err := client.Search(context.Background(), &proto.SearchRequest{Query: []float32{0.1}, K: 2})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(resp.Results))
	}
	if resp.Results[0].ID != 10 {
		t.Errorf("expected ID 10, got %d", resp.Results[0].ID)
	}
	if conn.lastMethod != proto.VectorIndexService_Search_FullMethodName {
		t.Errorf("unexpected method: %s", conn.lastMethod)
	}
}

func TestVectorIndexServiceClient_Search_RPCError(t *testing.T) {
	sentinel := errors.New("rpc search error")
	conn := &fakeConn{wantErr: sentinel}
	_, err := newClientConn(conn).Search(context.Background(), &proto.SearchRequest{K: 5})
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// BatchInsert
// ---------------------------------------------------------------------------

func TestVectorIndexServiceClient_BatchInsert_Success(t *testing.T) {
	payload := jsonOf(&proto.BatchInsertResponse{Count: 3})
	conn := &fakeConn{respPayload: payload}
	client := newClientConn(conn)

	resp, err := client.BatchInsert(context.Background(), &proto.BatchInsertRequest{
		Items: []*proto.InsertRequest{
			{ID: 1, Vector: []float32{0.1}},
			{ID: 2, Vector: []float32{0.2}},
			{ID: 3, Vector: []float32{0.3}},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Count != 3 {
		t.Errorf("expected Count 3, got %d", resp.Count)
	}
	if conn.lastMethod != proto.VectorIndexService_BatchInsert_FullMethodName {
		t.Errorf("unexpected method: %s", conn.lastMethod)
	}
}

func TestVectorIndexServiceClient_BatchInsert_RPCError(t *testing.T) {
	sentinel := errors.New("rpc batch error")
	conn := &fakeConn{wantErr: sentinel}
	_, err := newClientConn(conn).BatchInsert(context.Background(), &proto.BatchInsertRequest{})
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Count
// ---------------------------------------------------------------------------

func TestVectorIndexServiceClient_Count_Success(t *testing.T) {
	payload := jsonOf(&proto.CountResponse{Count: 42})
	conn := &fakeConn{respPayload: payload}
	client := newClientConn(conn)

	resp, err := client.Count(context.Background(), &proto.CountRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Count != 42 {
		t.Errorf("expected Count 42, got %d", resp.Count)
	}
	if conn.lastMethod != proto.VectorIndexService_Count_FullMethodName {
		t.Errorf("unexpected method: %s", conn.lastMethod)
	}
}

func TestVectorIndexServiceClient_Count_RPCError(t *testing.T) {
	sentinel := errors.New("rpc count error")
	conn := &fakeConn{wantErr: sentinel}
	_, err := newClientConn(conn).Count(context.Background(), &proto.CountRequest{})
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// GetStats
// ---------------------------------------------------------------------------

func TestVectorIndexServiceClient_GetStats_Success(t *testing.T) {
	payload := jsonOf(&proto.StatsResponse{Count: 100, Dimensions: 128, Capacity: 1000})
	conn := &fakeConn{respPayload: payload}
	client := newClientConn(conn)

	resp, err := client.GetStats(context.Background(), &proto.StatsRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Count != 100 {
		t.Errorf("expected Count 100, got %d", resp.Count)
	}
	if resp.Dimensions != 128 {
		t.Errorf("expected Dimensions 128, got %d", resp.Dimensions)
	}
	if resp.Capacity != 1000 {
		t.Errorf("expected Capacity 1000, got %d", resp.Capacity)
	}
	if conn.lastMethod != proto.VectorIndexService_GetStats_FullMethodName {
		t.Errorf("unexpected method: %s", conn.lastMethod)
	}
}

func TestVectorIndexServiceClient_GetStats_RPCError(t *testing.T) {
	sentinel := errors.New("rpc stats error")
	conn := &fakeConn{wantErr: sentinel}
	_, err := newClientConn(conn).GetStats(context.Background(), &proto.StatsRequest{})
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// invoke — internal method coverage via table-driven tests
// ---------------------------------------------------------------------------

func TestInvoke_EmptyResponseBody(t *testing.T) {
	// When the server returns zero bytes, invoke must not attempt unmarshal
	// and must return nil.
	conn := &fakeConn{} // respPayload is nil → empty body path
	client := newClientConn(conn)

	// GetStats with empty body — resp stays zero-valued, no error.
	resp, err := client.GetStats(context.Background(), &proto.StatsRequest{})
	if err != nil {
		t.Fatalf("expected nil error on empty body, got: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil StatsResponse even on empty body")
	}
}

func TestInvoke_InvalidJSONResponse(t *testing.T) {
	// Confirm that corrupt JSON in the response body propagates as an error.
	conn := &fakeConn{respPayload: []byte("{invalid")}
	client := newClientConn(conn)

	_, err := client.Count(context.Background(), &proto.CountRequest{})
	if err == nil {
		t.Fatal("expected unmarshal error for corrupt JSON response")
	}
}
