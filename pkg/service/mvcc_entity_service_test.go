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
	"context"
	"errors"
	"testing"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// ---------------------------------------------------------------------------
// PutEntity tests
// ---------------------------------------------------------------------------

func TestMVCCKVService_PutEntity_Success(t *testing.T) {
	var capturedCmd []byte

	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, cmd []byte) (sm.Result, error) {
			capturedCmd = make([]byte, len(cmd))
			copy(capturedCmd, cmd)
			return sm.Result{Value: 1}, nil
		},
		syncReadFn: func(_ context.Context, _ uint64, query interface{}) (interface{}, error) {
			if _, ok := query.(MVCCRevisionQuery); ok {
				return int64(10), nil
			}
			return nil, nil
		},
	}

	svc := NewMVCCKVService(host, 1)
	resp, err := svc.PutEntity(context.Background(), &PutEntityRequest{
		EntityType: "user",
		ID:         "u42",
		Data:       []byte(`{"name":"alice"}`),
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if resp.Revision != 10 {
		t.Fatalf("expected revision 10, got %d", resp.Revision)
	}
	if resp.Timestamp <= 0 {
		t.Fatal("expected positive timestamp")
	}

	// Verify the key was encoded as "user/u42".
	expected := mustEncode(protocol.EncodeMVCCPut([]byte("user/u42"), []byte(`{"name":"alice"}`), 0))
	if len(capturedCmd) != len(expected) {
		t.Fatalf("expected cmd length %d, got %d", len(expected), len(capturedCmd))
	}
}

func TestMVCCKVService_PutEntity_EmptyType(t *testing.T) {
	host := &mockHost{}
	svc := NewMVCCKVService(host, 1)

	_, err := svc.PutEntity(context.Background(), &PutEntityRequest{
		EntityType: "",
		ID:         "u1",
		Data:       []byte("data"),
	})
	if err == nil {
		t.Fatal("expected error for empty entity type, got nil")
	}

	var emptyTypeErr *EntityEmptyTypeError
	if !errors.As(err, &emptyTypeErr) {
		t.Fatalf("expected *EntityEmptyTypeError, got %T: %v", err, err)
	}
}

func TestMVCCKVService_PutEntity_EmptyID(t *testing.T) {
	host := &mockHost{}
	svc := NewMVCCKVService(host, 1)

	_, err := svc.PutEntity(context.Background(), &PutEntityRequest{
		EntityType: "user",
		ID:         "",
		Data:       []byte("data"),
	})
	if err == nil {
		t.Fatal("expected error for empty ID, got nil")
	}

	var emptyIDErr *EntityEmptyIDError
	if !errors.As(err, &emptyIDErr) {
		t.Fatalf("expected *EntityEmptyIDError, got %T: %v", err, err)
	}
	if emptyIDErr.EntityType != "user" {
		t.Fatalf("expected EntityType 'user', got %q", emptyIDErr.EntityType)
	}
}

func TestMVCCKVService_PutEntity_EmptyData(t *testing.T) {
	host := &mockHost{}
	svc := NewMVCCKVService(host, 1)

	_, err := svc.PutEntity(context.Background(), &PutEntityRequest{
		EntityType: "user",
		ID:         "u1",
		Data:       nil,
	})
	if err == nil {
		t.Fatal("expected error for nil data, got nil")
	}

	var emptyDataErr *EntityEmptyDataError
	if !errors.As(err, &emptyDataErr) {
		t.Fatalf("expected *EntityEmptyDataError, got %T: %v", err, err)
	}
}

func TestMVCCKVService_PutEntity_ProposeError(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{}, &quicraft.NotLeaderError{
				ShardID:       1,
				LeaderID:      2,
				LeaderAddress: "10.0.0.2:9100",
			}
		},
	}

	svc := NewMVCCKVService(host, 1)
	_, err := svc.PutEntity(context.Background(), &PutEntityRequest{
		EntityType: "user",
		ID:         "u1",
		Data:       []byte("payload"),
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("expected *NotLeaderError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// GetEntity tests
// ---------------------------------------------------------------------------

func TestMVCCKVService_GetEntity_Success(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, query interface{}) (interface{}, error) {
			switch query.(type) {
			case MVCCGetQuery:
				return &KeyValue{
					Key:   []byte("config/c1"),
					Value: []byte(`{"ttl":60}`),
				}, nil
			case MVCCRevisionQuery:
				return int64(25), nil
			}
			return nil, nil
		},
	}

	svc := NewMVCCKVService(host, 1)
	resp, err := svc.GetEntity(context.Background(), &GetEntityRequest{
		EntityType: "config",
		ID:         "c1",
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if string(resp.Data) != `{"ttl":60}` {
		t.Fatalf("expected data '{\"ttl\":60}', got %q", resp.Data)
	}
	if resp.Revision != 25 {
		t.Fatalf("expected revision 25, got %d", resp.Revision)
	}
	if resp.Timestamp <= 0 {
		t.Fatal("expected positive timestamp")
	}
}

func TestMVCCKVService_GetEntity_EmptyType(t *testing.T) {
	host := &mockHost{}
	svc := NewMVCCKVService(host, 1)

	_, err := svc.GetEntity(context.Background(), &GetEntityRequest{
		EntityType: "",
		ID:         "c1",
	})
	if err == nil {
		t.Fatal("expected error for empty entity type, got nil")
	}

	var emptyTypeErr *EntityEmptyTypeError
	if !errors.As(err, &emptyTypeErr) {
		t.Fatalf("expected *EntityEmptyTypeError, got %T: %v", err, err)
	}
}

func TestMVCCKVService_GetEntity_EmptyID(t *testing.T) {
	host := &mockHost{}
	svc := NewMVCCKVService(host, 1)

	_, err := svc.GetEntity(context.Background(), &GetEntityRequest{
		EntityType: "config",
		ID:         "",
	})
	if err == nil {
		t.Fatal("expected error for empty ID, got nil")
	}

	var emptyIDErr *EntityEmptyIDError
	if !errors.As(err, &emptyIDErr) {
		t.Fatalf("expected *EntityEmptyIDError, got %T: %v", err, err)
	}
	if emptyIDErr.EntityType != "config" {
		t.Fatalf("expected EntityType 'config', got %q", emptyIDErr.EntityType)
	}
}

func TestMVCCKVService_GetEntity_ReadError(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, query interface{}) (interface{}, error) {
			if _, ok := query.(MVCCGetQuery); ok {
				return nil, &mockKeyNotFoundError{key: "session/s99"}
			}
			return nil, nil
		},
	}

	svc := NewMVCCKVService(host, 1)
	_, err := svc.GetEntity(context.Background(), &GetEntityRequest{
		EntityType: "session",
		ID:         "s99",
	})
	if err == nil {
		t.Fatal("expected error for missing key, got nil")
	}

	var knf *KeyNotFoundError
	if !errors.As(err, &knf) {
		t.Fatalf("expected *KeyNotFoundError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// DeleteEntity tests
// ---------------------------------------------------------------------------

func TestMVCCKVService_DeleteEntity_Success(t *testing.T) {
	var capturedCmd []byte

	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, cmd []byte) (sm.Result, error) {
			capturedCmd = make([]byte, len(cmd))
			copy(capturedCmd, cmd)
			return sm.Result{}, nil
		},
		syncReadFn: func(_ context.Context, _ uint64, query interface{}) (interface{}, error) {
			if _, ok := query.(MVCCRevisionQuery); ok {
				return int64(30), nil
			}
			return nil, nil
		},
	}

	svc := NewMVCCKVService(host, 1)
	resp, err := svc.DeleteEntity(context.Background(), &DeleteEntityRequest{
		EntityType: "session",
		ID:         "s1",
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if !resp.Deleted {
		t.Fatal("expected Deleted=true")
	}
	if resp.Revision != 30 {
		t.Fatalf("expected revision 30, got %d", resp.Revision)
	}

	// Verify the key was encoded as "session/s1".
	expected := mustEncode(protocol.EncodeMVCCDelete([]byte("session/s1")))
	if len(capturedCmd) != len(expected) {
		t.Fatalf("expected cmd length %d, got %d", len(expected), len(capturedCmd))
	}
}

func TestMVCCKVService_DeleteEntity_EmptyType(t *testing.T) {
	host := &mockHost{}
	svc := NewMVCCKVService(host, 1)

	_, err := svc.DeleteEntity(context.Background(), &DeleteEntityRequest{
		EntityType: "",
		ID:         "s1",
	})
	if err == nil {
		t.Fatal("expected error for empty entity type, got nil")
	}

	var emptyTypeErr *EntityEmptyTypeError
	if !errors.As(err, &emptyTypeErr) {
		t.Fatalf("expected *EntityEmptyTypeError, got %T: %v", err, err)
	}
}

func TestMVCCKVService_DeleteEntity_EmptyID(t *testing.T) {
	host := &mockHost{}
	svc := NewMVCCKVService(host, 1)

	_, err := svc.DeleteEntity(context.Background(), &DeleteEntityRequest{
		EntityType: "session",
		ID:         "",
	})
	if err == nil {
		t.Fatal("expected error for empty ID, got nil")
	}

	var emptyIDErr *EntityEmptyIDError
	if !errors.As(err, &emptyIDErr) {
		t.Fatalf("expected *EntityEmptyIDError, got %T: %v", err, err)
	}
	if emptyIDErr.EntityType != "session" {
		t.Fatalf("expected EntityType 'session', got %q", emptyIDErr.EntityType)
	}
}

func TestMVCCKVService_DeleteEntity_ProposeError(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{}, errors.New("raft timeout")
		},
	}

	svc := NewMVCCKVService(host, 1)
	_, err := svc.DeleteEntity(context.Background(), &DeleteEntityRequest{
		EntityType: "user",
		ID:         "u1",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var pe *ProposeError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *ProposeError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// ListEntities tests
// ---------------------------------------------------------------------------

func TestMVCCKVService_ListEntities_Success(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, query interface{}) (interface{}, error) {
			if _, ok := query.(MVCCRangeQuery); ok {
				return &MVCCRangeResult{
					KeyValues: []*KeyValue{
						{Key: []byte("user/alice")},
						{Key: []byte("user/bob")},
						{Key: []byte("user/charlie")},
					},
					Revision: 50,
				}, nil
			}
			return nil, nil
		},
	}

	svc := NewMVCCKVService(host, 1)
	resp, err := svc.ListEntities(context.Background(), &ListEntitiesRequest{
		EntityType: "user",
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if len(resp.Keys) != 3 {
		t.Fatalf("expected 3 keys, got %d", len(resp.Keys))
	}
	// Keys should have the "user/" prefix stripped.
	if resp.Keys[0] != "alice" || resp.Keys[1] != "bob" || resp.Keys[2] != "charlie" {
		t.Fatalf("unexpected keys: %v", resp.Keys)
	}
	if resp.TotalCount != 3 {
		t.Fatalf("expected TotalCount 3, got %d", resp.TotalCount)
	}
}

func TestMVCCKVService_ListEntities_EmptyType(t *testing.T) {
	host := &mockHost{}
	svc := NewMVCCKVService(host, 1)

	_, err := svc.ListEntities(context.Background(), &ListEntitiesRequest{
		EntityType: "",
	})
	if err == nil {
		t.Fatal("expected error for empty entity type, got nil")
	}

	var emptyTypeErr *EntityEmptyTypeError
	if !errors.As(err, &emptyTypeErr) {
		t.Fatalf("expected *EntityEmptyTypeError, got %T: %v", err, err)
	}
}

func TestMVCCKVService_ListEntities_ReadError(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, errors.New("storage unavailable")
		},
	}

	svc := NewMVCCKVService(host, 1)
	_, err := svc.ListEntities(context.Background(), &ListEntitiesRequest{
		EntityType: "user",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
}

func TestMVCCKVService_ListEntities_WithPrefix(t *testing.T) {
	var capturedQuery interface{}

	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, query interface{}) (interface{}, error) {
			capturedQuery = query
			return &MVCCRangeResult{
				KeyValues: []*KeyValue{
					{Key: []byte("user/admin_alice")},
					{Key: []byte("user/admin_bob")},
				},
				Revision: 10,
			}, nil
		},
	}

	svc := NewMVCCKVService(host, 1)
	resp, err := svc.ListEntities(context.Background(), &ListEntitiesRequest{
		EntityType: "user",
		Prefix:     "admin_",
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	// Verify prefix was appended to entity prefix in the range query.
	rq, ok := capturedQuery.(MVCCRangeQuery)
	if !ok {
		t.Fatalf("expected MVCCRangeQuery, got %T", capturedQuery)
	}
	if string(rq.Start) != "user/admin_" {
		t.Fatalf("expected start 'user/admin_', got %q", string(rq.Start))
	}

	// Keys should still have "user/" prefix stripped.
	if len(resp.Keys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(resp.Keys))
	}
	if resp.Keys[0] != "admin_alice" || resp.Keys[1] != "admin_bob" {
		t.Fatalf("unexpected keys: %v", resp.Keys)
	}
}

func TestMVCCKVService_ListEntities_Pagination(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return &MVCCRangeResult{
				KeyValues: []*KeyValue{
					{Key: []byte("item/a")},
					{Key: []byte("item/b")},
					{Key: []byte("item/c")},
					{Key: []byte("item/d")},
					{Key: []byte("item/e")},
				},
				Revision: 20,
			}, nil
		},
	}

	svc := NewMVCCKVService(host, 1)

	// First page: 2 items.
	resp, err := svc.ListEntities(context.Background(), &ListEntitiesRequest{
		EntityType: "item",
		PageSize:   2,
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if len(resp.Keys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(resp.Keys))
	}
	if resp.Keys[0] != "a" || resp.Keys[1] != "b" {
		t.Fatalf("unexpected first page keys: %v", resp.Keys)
	}
	if resp.NextPageToken == "" {
		t.Fatal("expected non-empty NextPageToken for partial page")
	}
	if resp.TotalCount != 5 {
		t.Fatalf("expected TotalCount 5, got %d", resp.TotalCount)
	}

	// Second page using the token.
	resp2, err := svc.ListEntities(context.Background(), &ListEntitiesRequest{
		EntityType: "item",
		PageSize:   2,
		PageToken:  resp.NextPageToken,
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if len(resp2.Keys) != 2 {
		t.Fatalf("expected 2 keys on second page, got %d", len(resp2.Keys))
	}
	if resp2.Keys[0] != "c" || resp2.Keys[1] != "d" {
		t.Fatalf("unexpected second page keys: %v", resp2.Keys)
	}

	// Third page: last item, no next token.
	resp3, err := svc.ListEntities(context.Background(), &ListEntitiesRequest{
		EntityType: "item",
		PageSize:   2,
		PageToken:  resp2.NextPageToken,
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if len(resp3.Keys) != 1 {
		t.Fatalf("expected 1 key on last page, got %d", len(resp3.Keys))
	}
	if resp3.Keys[0] != "e" {
		t.Fatalf("unexpected last page key: %v", resp3.Keys)
	}
	if resp3.NextPageToken != "" {
		t.Fatalf("expected empty NextPageToken on last page, got %q", resp3.NextPageToken)
	}
}

func TestMVCCKVService_ListEntities_InvalidPageToken(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return &MVCCRangeResult{
				KeyValues: []*KeyValue{
					{Key: []byte("user/a")},
				},
				Revision: 1,
			}, nil
		},
	}

	svc := NewMVCCKVService(host, 1)
	_, err := svc.ListEntities(context.Background(), &ListEntitiesRequest{
		EntityType: "user",
		PageToken:  "not-a-number",
	})
	if err == nil {
		t.Fatal("expected error for invalid page token, got nil")
	}

	var tokenErr *InvalidPageTokenError
	if !errors.As(err, &tokenErr) {
		t.Fatalf("expected *InvalidPageTokenError, got %T: %v", err, err)
	}
	if tokenErr.Token != "not-a-number" {
		t.Fatalf("expected token 'not-a-number', got %q", tokenErr.Token)
	}
}

func TestMVCCKVService_ListEntities_PageTokenBeyondRange(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return &MVCCRangeResult{
				KeyValues: []*KeyValue{
					{Key: []byte("user/a")},
					{Key: []byte("user/b")},
				},
				Revision: 1,
			}, nil
		},
	}

	svc := NewMVCCKVService(host, 1)
	resp, err := svc.ListEntities(context.Background(), &ListEntitiesRequest{
		EntityType: "user",
		PageToken:  "999",
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if len(resp.Keys) != 0 {
		t.Fatalf("expected 0 keys for out-of-range token, got %d", len(resp.Keys))
	}
}

func TestMVCCKVService_ListEntities_EmptyResult(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return &MVCCRangeResult{
				KeyValues: []*KeyValue{},
				Revision:  0,
			}, nil
		},
	}

	svc := NewMVCCKVService(host, 1)
	resp, err := svc.ListEntities(context.Background(), &ListEntitiesRequest{
		EntityType: "user",
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if len(resp.Keys) != 0 {
		t.Fatalf("expected 0 keys, got %d", len(resp.Keys))
	}
	if resp.TotalCount != 0 {
		t.Fatalf("expected TotalCount 0, got %d", resp.TotalCount)
	}
	if resp.NextPageToken != "" {
		t.Fatalf("expected empty NextPageToken, got %q", resp.NextPageToken)
	}
}

// mustEncode unwraps an encoder's (command, error) pair for tests whose
// inputs are small and fixed. An error here is a TEST bug -- the encoders
// refuse only values their length fields cannot describe -- so it fails
// loudly rather than being threaded through every call site.
func mustEncode(cmd []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return cmd
}
