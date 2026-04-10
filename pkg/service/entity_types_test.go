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
	"sort"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"
)

// ---------------------------------------------------------------------------
// entityKey / entityPrefix helper tests
// ---------------------------------------------------------------------------

func TestEntityKey(t *testing.T) {
	tests := []struct {
		name       string
		entityType EntityType
		id         string
		want       string
	}{
		{
			name:       "simple user entity",
			entityType: "user",
			id:         "123",
			want:       "user/123",
		},
		{
			name:       "config entity with dashes",
			entityType: "config",
			id:         "app-settings-v2",
			want:       "config/app-settings-v2",
		},
		{
			name:       "nested entity type",
			entityType: "org.team",
			id:         "alpha",
			want:       "org.team/alpha",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := entityKey(tt.entityType, tt.id)
			if got != tt.want {
				t.Fatalf("entityKey(%q, %q) = %q, want %q", tt.entityType, tt.id, got, tt.want)
			}
		})
	}
}

func TestEntityPrefix(t *testing.T) {
	tests := []struct {
		name       string
		entityType EntityType
		want       string
	}{
		{
			name:       "user prefix",
			entityType: "user",
			want:       "user/",
		},
		{
			name:       "config prefix",
			entityType: "config",
			want:       "config/",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := entityPrefix(tt.entityType)
			if got != tt.want {
				t.Fatalf("entityPrefix(%q) = %q, want %q", tt.entityType, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// PutEntity tests
// ---------------------------------------------------------------------------

func TestPutEntity_Success(t *testing.T) {
	var capturedKey string
	var capturedValue []byte

	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, cmd []byte) (sm.Result, error) {
			// Decode the Put command to verify key/value.
			decoded, decErr := kv.DecodeCommand(cmd)
			if decErr != nil {
				t.Fatalf("DecodeCommand error: %v", decErr)
			}
			if decoded.Op != kv.OpPut {
				t.Fatalf("expected OpPut, got %d", decoded.Op)
			}
			capturedKey = string(decoded.Key)
			capturedValue = make([]byte, len(decoded.Value))
			copy(capturedValue, decoded.Value)
			return sm.Result{Value: 1}, nil
		},
	}

	svc := NewKVService(host, 1)
	resp, err := svc.PutEntity(context.Background(), &PutEntityRequest{
		EntityType: "user",
		ID:         "456",
		Data:       []byte(`{"name":"alice"}`),
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if resp.Version != 1 {
		t.Fatalf("expected Version 1, got %d", resp.Version)
	}
	if resp.Timestamp <= 0 {
		t.Fatal("expected positive Timestamp")
	}
	if capturedKey != "user/456" {
		t.Fatalf("expected stored key %q, got %q", "user/456", capturedKey)
	}
	if string(capturedValue) != `{"name":"alice"}` {
		t.Fatalf("expected stored value %q, got %q", `{"name":"alice"}`, string(capturedValue))
	}
}

func TestPutEntity_EmptyData(t *testing.T) {
	host := &mockHost{}
	svc := NewKVService(host, 1)

	_, err := svc.PutEntity(context.Background(), &PutEntityRequest{
		EntityType: "user",
		ID:         "789",
		Data:       nil,
	})
	if err == nil {
		t.Fatal("expected error for nil data")
	}

	var emptyDataErr *EntityEmptyDataError
	if !errors.As(err, &emptyDataErr) {
		t.Fatalf("expected EntityEmptyDataError, got %T: %v", err, err)
	}
	if emptyDataErr.EntityType != "user" {
		t.Fatalf("expected EntityType %q, got %q", "user", emptyDataErr.EntityType)
	}
	if emptyDataErr.ID != "789" {
		t.Fatalf("expected ID %q, got %q", "789", emptyDataErr.ID)
	}
}

func TestPutEntity_EmptyZeroLengthData(t *testing.T) {
	host := &mockHost{}
	svc := NewKVService(host, 1)

	_, err := svc.PutEntity(context.Background(), &PutEntityRequest{
		EntityType: "user",
		ID:         "789",
		Data:       []byte{},
	})
	if err == nil {
		t.Fatal("expected error for zero-length data")
	}

	var emptyDataErr *EntityEmptyDataError
	if !errors.As(err, &emptyDataErr) {
		t.Fatalf("expected EntityEmptyDataError, got %T: %v", err, err)
	}
}

func TestPutEntity_EmptyEntityType(t *testing.T) {
	host := &mockHost{}
	svc := NewKVService(host, 1)

	_, err := svc.PutEntity(context.Background(), &PutEntityRequest{
		EntityType: "",
		ID:         "123",
		Data:       []byte("data"),
	})
	if err == nil {
		t.Fatal("expected error for empty entity type")
	}

	var emptyTypeErr *EntityEmptyTypeError
	if !errors.As(err, &emptyTypeErr) {
		t.Fatalf("expected EntityEmptyTypeError, got %T: %v", err, err)
	}
}

func TestPutEntity_EmptyID(t *testing.T) {
	host := &mockHost{}
	svc := NewKVService(host, 1)

	_, err := svc.PutEntity(context.Background(), &PutEntityRequest{
		EntityType: "user",
		ID:         "",
		Data:       []byte("data"),
	})
	if err == nil {
		t.Fatal("expected error for empty ID")
	}

	var emptyIDErr *EntityEmptyIDError
	if !errors.As(err, &emptyIDErr) {
		t.Fatalf("expected EntityEmptyIDError, got %T: %v", err, err)
	}
	if emptyIDErr.EntityType != "user" {
		t.Fatalf("expected EntityType %q, got %q", "user", emptyIDErr.EntityType)
	}
}

func TestPutEntity_ProposeError(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{}, &proposeTestError{}
		},
	}

	svc := NewKVService(host, 1)
	_, err := svc.PutEntity(context.Background(), &PutEntityRequest{
		EntityType: "user",
		ID:         "123",
		Data:       []byte("data"),
	})
	if err == nil {
		t.Fatal("expected error from propose failure")
	}

	var propErr *ProposeError
	if !errors.As(err, &propErr) {
		t.Fatalf("expected ProposeError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// GetEntity tests
// ---------------------------------------------------------------------------

func TestGetEntity_Success(t *testing.T) {
	// Store data keyed by the entity key format.
	store := map[string][]byte{
		"user/123": []byte(`{"name":"bob"}`),
	}

	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, query interface{}) (interface{}, error) {
			key, ok := query.([]byte)
			if !ok {
				return nil, &unexpectedResultTypeError{}
			}
			val, found := store[string(key)]
			if !found {
				return nil, &kv.KeyNotFoundError{Key: string(key)}
			}
			return val, nil
		},
	}

	svc := NewKVService(host, 1)
	resp, err := svc.GetEntity(context.Background(), &GetEntityRequest{
		EntityType: "user",
		ID:         "123",
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if string(resp.Data) != `{"name":"bob"}` {
		t.Fatalf("expected data %q, got %q", `{"name":"bob"}`, string(resp.Data))
	}
	if resp.Version != 1 {
		t.Fatalf("expected Version 1, got %d", resp.Version)
	}
	if resp.Timestamp <= 0 {
		t.Fatal("expected positive Timestamp")
	}
}

func TestGetEntity_NotFound(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, query interface{}) (interface{}, error) {
			key := string(query.([]byte))
			return nil, &kv.KeyNotFoundError{Key: key}
		},
	}

	svc := NewKVService(host, 1)
	_, err := svc.GetEntity(context.Background(), &GetEntityRequest{
		EntityType: "user",
		ID:         "nonexistent",
	})
	if err == nil {
		t.Fatal("expected error for non-existent entity")
	}

	var knfErr *KeyNotFoundError
	if !errors.As(err, &knfErr) {
		t.Fatalf("expected KeyNotFoundError, got %T: %v", err, err)
	}
	if knfErr.Key != "user/nonexistent" {
		t.Fatalf("expected key %q, got %q", "user/nonexistent", knfErr.Key)
	}
}

func TestGetEntity_EmptyEntityType(t *testing.T) {
	host := &mockHost{}
	svc := NewKVService(host, 1)

	_, err := svc.GetEntity(context.Background(), &GetEntityRequest{
		EntityType: "",
		ID:         "123",
	})
	if err == nil {
		t.Fatal("expected error for empty entity type")
	}

	var emptyTypeErr *EntityEmptyTypeError
	if !errors.As(err, &emptyTypeErr) {
		t.Fatalf("expected EntityEmptyTypeError, got %T: %v", err, err)
	}
}

func TestGetEntity_EmptyID(t *testing.T) {
	host := &mockHost{}
	svc := NewKVService(host, 1)

	_, err := svc.GetEntity(context.Background(), &GetEntityRequest{
		EntityType: "user",
		ID:         "",
	})
	if err == nil {
		t.Fatal("expected error for empty ID")
	}

	var emptyIDErr *EntityEmptyIDError
	if !errors.As(err, &emptyIDErr) {
		t.Fatalf("expected EntityEmptyIDError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// DeleteEntity tests
// ---------------------------------------------------------------------------

func TestDeleteEntity_Success(t *testing.T) {
	var capturedCmd []byte

	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, cmd []byte) (sm.Result, error) {
			capturedCmd = make([]byte, len(cmd))
			copy(capturedCmd, cmd)
			return sm.Result{Value: 1}, nil
		},
	}

	svc := NewKVService(host, 1)
	resp, err := svc.DeleteEntity(context.Background(), &DeleteEntityRequest{
		EntityType: "user",
		ID:         "123",
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if !resp.Deleted {
		t.Fatal("expected Deleted to be true")
	}

	// Verify the command encodes the correct key.
	expected, encErr := kv.EncodeDelete([]byte("user/123"))
	if encErr != nil {
		t.Fatalf("EncodeDelete error: %v", encErr)
	}
	if len(capturedCmd) != len(expected) {
		t.Fatalf("expected cmd length %d, got %d", len(expected), len(capturedCmd))
	}
	for i := range expected {
		if capturedCmd[i] != expected[i] {
			t.Fatalf("cmd mismatch at byte %d: expected %d, got %d", i, expected[i], capturedCmd[i])
		}
	}
}

func TestDeleteEntity_EmptyEntityType(t *testing.T) {
	host := &mockHost{}
	svc := NewKVService(host, 1)

	_, err := svc.DeleteEntity(context.Background(), &DeleteEntityRequest{
		EntityType: "",
		ID:         "123",
	})
	if err == nil {
		t.Fatal("expected error for empty entity type")
	}

	var emptyTypeErr *EntityEmptyTypeError
	if !errors.As(err, &emptyTypeErr) {
		t.Fatalf("expected EntityEmptyTypeError, got %T: %v", err, err)
	}
}

func TestDeleteEntity_EmptyID(t *testing.T) {
	host := &mockHost{}
	svc := NewKVService(host, 1)

	_, err := svc.DeleteEntity(context.Background(), &DeleteEntityRequest{
		EntityType: "user",
		ID:         "",
	})
	if err == nil {
		t.Fatal("expected error for empty ID")
	}

	var emptyIDErr *EntityEmptyIDError
	if !errors.As(err, &emptyIDErr) {
		t.Fatalf("expected EntityEmptyIDError, got %T: %v", err, err)
	}
}

func TestDeleteEntity_ProposeError(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{}, &proposeTestError{}
		},
	}

	svc := NewKVService(host, 1)
	_, err := svc.DeleteEntity(context.Background(), &DeleteEntityRequest{
		EntityType: "user",
		ID:         "123",
	})
	if err == nil {
		t.Fatal("expected error from propose failure")
	}

	var propErr *ProposeError
	if !errors.As(err, &propErr) {
		t.Fatalf("expected ProposeError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// ListEntities tests
// ---------------------------------------------------------------------------

func TestListEntities_Basic(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, query interface{}) (interface{}, error) {
			lq, ok := query.(kv.ListQuery)
			if !ok {
				return nil, &unexpectedResultTypeError{}
			}
			// Simulate stored keys with the "user/" prefix.
			allKeys := []string{"user/alice", "user/bob", "user/charlie"}
			var matched []string
			for _, k := range allKeys {
				if strings.HasPrefix(k, lq.Prefix) {
					matched = append(matched, k)
				}
			}
			return matched, nil
		},
	}

	svc := NewKVService(host, 1)
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

	// Keys should have the entity prefix stripped.
	sort.Strings(resp.Keys)
	expected := []string{"alice", "bob", "charlie"}
	for i, k := range resp.Keys {
		if k != expected[i] {
			t.Fatalf("key[%d] = %q, want %q", i, k, expected[i])
		}
	}
	if resp.TotalCount != 3 {
		t.Fatalf("expected TotalCount 3, got %d", resp.TotalCount)
	}
	if resp.NextPageToken != "" {
		t.Fatalf("expected empty NextPageToken, got %q", resp.NextPageToken)
	}
}

func TestListEntities_Pagination(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, query interface{}) (interface{}, error) {
			lq, ok := query.(kv.ListQuery)
			if !ok {
				return nil, &unexpectedResultTypeError{}
			}
			allKeys := []string{
				"item/a", "item/b", "item/c", "item/d", "item/e",
			}
			var matched []string
			for _, k := range allKeys {
				if strings.HasPrefix(k, lq.Prefix) {
					matched = append(matched, k)
				}
			}
			return matched, nil
		},
	}

	svc := NewKVService(host, 1)

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
		t.Fatalf("expected keys [a, b], got %v", resp.Keys)
	}
	if resp.NextPageToken == "" {
		t.Fatal("expected non-empty NextPageToken for first page")
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
		t.Fatalf("expected 2 keys, got %d", len(resp2.Keys))
	}
	if resp2.Keys[0] != "c" || resp2.Keys[1] != "d" {
		t.Fatalf("expected keys [c, d], got %v", resp2.Keys)
	}
	if resp2.NextPageToken == "" {
		t.Fatal("expected non-empty NextPageToken for second page")
	}

	// Third page: only 1 remaining.
	resp3, err := svc.ListEntities(context.Background(), &ListEntitiesRequest{
		EntityType: "item",
		PageSize:   2,
		PageToken:  resp2.NextPageToken,
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if len(resp3.Keys) != 1 {
		t.Fatalf("expected 1 key, got %d", len(resp3.Keys))
	}
	if resp3.Keys[0] != "e" {
		t.Fatalf("expected key %q, got %q", "e", resp3.Keys[0])
	}
	if resp3.NextPageToken != "" {
		t.Fatalf("expected empty NextPageToken on last page, got %q", resp3.NextPageToken)
	}
}

func TestListEntities_EmptyEntityType(t *testing.T) {
	host := &mockHost{}
	svc := NewKVService(host, 1)

	_, err := svc.ListEntities(context.Background(), &ListEntitiesRequest{
		EntityType: "",
	})
	if err == nil {
		t.Fatal("expected error for empty entity type")
	}

	var emptyTypeErr *EntityEmptyTypeError
	if !errors.As(err, &emptyTypeErr) {
		t.Fatalf("expected EntityEmptyTypeError, got %T: %v", err, err)
	}
}

func TestListEntities_EmptyResult(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return []string{}, nil
		},
	}

	svc := NewKVService(host, 1)
	resp, err := svc.ListEntities(context.Background(), &ListEntitiesRequest{
		EntityType: "empty",
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
}

func TestListEntities_InvalidPageToken(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return []string{"x/a"}, nil
		},
	}

	svc := NewKVService(host, 1)
	_, err := svc.ListEntities(context.Background(), &ListEntitiesRequest{
		EntityType: "x",
		PageToken:  "not-a-number",
	})
	if err == nil {
		t.Fatal("expected error for invalid page token")
	}

	var tokenErr *InvalidPageTokenError
	if !errors.As(err, &tokenErr) {
		t.Fatalf("expected InvalidPageTokenError, got %T: %v", err, err)
	}
	if tokenErr.Token != "not-a-number" {
		t.Fatalf("expected token %q, got %q", "not-a-number", tokenErr.Token)
	}
}

func TestListEntities_PrefixFilter(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, query interface{}) (interface{}, error) {
			lq := query.(kv.ListQuery)
			allKeys := []string{
				"config/app-db-host", "config/app-db-port",
				"config/app-cache-ttl", "config/web-timeout",
			}
			var matched []string
			for _, k := range allKeys {
				if strings.HasPrefix(k, lq.Prefix) {
					matched = append(matched, k)
				}
			}
			return matched, nil
		},
	}

	svc := NewKVService(host, 1)
	resp, err := svc.ListEntities(context.Background(), &ListEntitiesRequest{
		EntityType: "config",
		Prefix:     "app-db",
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if len(resp.Keys) != 2 {
		t.Fatalf("expected 2 keys, got %d: %v", len(resp.Keys), resp.Keys)
	}
}

func TestListEntities_ReadError(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, &readTestError{}
		},
	}

	svc := NewKVService(host, 1)
	_, err := svc.ListEntities(context.Background(), &ListEntitiesRequest{
		EntityType: "user",
	})
	if err == nil {
		t.Fatal("expected error from read failure")
	}

	var readErr *ReadError
	if !errors.As(err, &readErr) {
		t.Fatalf("expected ReadError, got %T: %v", err, err)
	}
}

func TestListEntities_PageTokenBeyondRange(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return []string{"t/a", "t/b"}, nil
		},
	}

	svc := NewKVService(host, 1)
	resp, err := svc.ListEntities(context.Background(), &ListEntitiesRequest{
		EntityType: "t",
		PageToken:  "999",
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if len(resp.Keys) != 0 {
		t.Fatalf("expected 0 keys for out-of-range token, got %d", len(resp.Keys))
	}
}

// ---------------------------------------------------------------------------
// Entity error message tests
// ---------------------------------------------------------------------------

func TestEntityEmptyDataError_Message(t *testing.T) {
	err := &EntityEmptyDataError{EntityType: "user", ID: "42"}
	want := "service: entity data must not be empty: user/42"
	if err.Error() != want {
		t.Fatalf("got %q, want %q", err.Error(), want)
	}
}

func TestEntityEmptyIDError_Message(t *testing.T) {
	err := &EntityEmptyIDError{EntityType: "config"}
	want := "service: entity ID must not be empty for type: config"
	if err.Error() != want {
		t.Fatalf("got %q, want %q", err.Error(), want)
	}
}

func TestEntityEmptyTypeError_Message(t *testing.T) {
	err := &EntityEmptyTypeError{}
	want := "service: entity type must not be empty"
	if err.Error() != want {
		t.Fatalf("got %q, want %q", err.Error(), want)
	}
}

func TestInvalidPageTokenError_Message(t *testing.T) {
	err := &InvalidPageTokenError{Token: "bad"}
	want := "service: invalid page token: bad"
	if err.Error() != want {
		t.Fatalf("got %q, want %q", err.Error(), want)
	}
}

// ---------------------------------------------------------------------------
// validateEntityWrite tests
// ---------------------------------------------------------------------------

func TestValidateEntityWrite_AllValid(t *testing.T) {
	err := validateEntityWrite("user", "123", []byte("data"))
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestValidateEntityWrite_EmptyType(t *testing.T) {
	err := validateEntityWrite("", "123", []byte("data"))
	if err == nil {
		t.Fatal("expected error for empty type")
	}
	var emptyTypeErr *EntityEmptyTypeError
	if !errors.As(err, &emptyTypeErr) {
		t.Fatalf("expected EntityEmptyTypeError, got %T", err)
	}
}

func TestValidateEntityWrite_EmptyID(t *testing.T) {
	err := validateEntityWrite("user", "", []byte("data"))
	if err == nil {
		t.Fatal("expected error for empty ID")
	}
	var emptyIDErr *EntityEmptyIDError
	if !errors.As(err, &emptyIDErr) {
		t.Fatalf("expected EntityEmptyIDError, got %T", err)
	}
}

func TestValidateEntityWrite_NilData(t *testing.T) {
	err := validateEntityWrite("user", "123", nil)
	if err == nil {
		t.Fatal("expected error for nil data")
	}
	var emptyDataErr *EntityEmptyDataError
	if !errors.As(err, &emptyDataErr) {
		t.Fatalf("expected EntityEmptyDataError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// Integration: PutEntity then GetEntity round-trip
// ---------------------------------------------------------------------------

func TestPutGetEntity_RoundTrip(t *testing.T) {
	store := make(map[string][]byte)

	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, cmd []byte) (sm.Result, error) {
			decoded, decErr := kv.DecodeCommand(cmd)
			if decErr != nil {
				t.Fatalf("DecodeCommand error: %v", decErr)
			}
			switch decoded.Op {
			case kv.OpPut:
				store[string(decoded.Key)] = decoded.Value
			case kv.OpDelete:
				delete(store, string(decoded.Key))
			}
			return sm.Result{Value: 1}, nil
		},
		syncReadFn: func(_ context.Context, _ uint64, query interface{}) (interface{}, error) {
			switch q := query.(type) {
			case []byte:
				val, ok := store[string(q)]
				if !ok {
					return nil, &kv.KeyNotFoundError{Key: string(q)}
				}
				return val, nil
			case kv.ListQuery:
				var keys []string
				for k := range store {
					if strings.HasPrefix(k, q.Prefix) {
						keys = append(keys, k)
					}
				}
				sort.Strings(keys)
				return keys, nil
			default:
				return nil, &unexpectedResultTypeError{}
			}
		},
	}

	svc := NewKVService(host, 1)
	ctx := context.Background()

	// Put an entity.
	_, err := svc.PutEntity(ctx, &PutEntityRequest{
		EntityType: "session",
		ID:         "abc",
		Data:       []byte("session-data"),
	})
	if err != nil {
		t.Fatalf("PutEntity failed: %v", err)
	}

	// Get it back.
	resp, err := svc.GetEntity(ctx, &GetEntityRequest{
		EntityType: "session",
		ID:         "abc",
	})
	if err != nil {
		t.Fatalf("GetEntity failed: %v", err)
	}
	if string(resp.Data) != "session-data" {
		t.Fatalf("expected %q, got %q", "session-data", string(resp.Data))
	}

	// Delete it.
	delResp, err := svc.DeleteEntity(ctx, &DeleteEntityRequest{
		EntityType: "session",
		ID:         "abc",
	})
	if err != nil {
		t.Fatalf("DeleteEntity failed: %v", err)
	}
	if !delResp.Deleted {
		t.Fatal("expected Deleted to be true")
	}

	// Verify it's gone.
	_, err = svc.GetEntity(ctx, &GetEntityRequest{
		EntityType: "session",
		ID:         "abc",
	})
	if err == nil {
		t.Fatal("expected error after delete")
	}
	var knfErr *KeyNotFoundError
	if !errors.As(err, &knfErr) {
		t.Fatalf("expected KeyNotFoundError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Test helpers (local to this file)
// ---------------------------------------------------------------------------

// proposeTestError is a simple error used to simulate Raft propose failures.
type proposeTestError struct{}

func (e *proposeTestError) Error() string { return "test: propose failed" }

// readTestError is a simple error used to simulate read failures.
type readTestError struct{}

func (e *readTestError) Error() string { return "test: read failed" }
