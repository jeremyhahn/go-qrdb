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
	"errors"
	"testing"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// protoToWatchEvent tests
// ---------------------------------------------------------------------------

func TestProtoToWatchEvent_Nil(t *testing.T) {
	result := protoToWatchEvent(nil)
	if result != nil {
		t.Errorf("protoToWatchEvent(nil) = %v, want nil", result)
	}
}

func TestProtoToWatchEvent_PutEvent(t *testing.T) {
	ev := &pb.Event{
		Type: pb.EventType_EVENT_PUT,
		Kv: &pb.KeyValue{
			Key:            []byte("key1"),
			Value:          []byte("value1"),
			CreateRevision: 1,
			ModRevision:    2,
			Version:        1,
		},
	}
	result := protoToWatchEvent(ev)
	if result == nil {
		t.Fatal("protoToWatchEvent() returned nil for valid input")
	}
	if result.Type != transport.EventPut {
		t.Errorf("Type = %v, want EventPut", result.Type)
	}
	if result.Kv == nil {
		t.Fatal("Kv is nil")
	}
	if string(result.Kv.Key) != "key1" {
		t.Errorf("Kv.Key = %q, want %q", result.Kv.Key, "key1")
	}
	if result.PrevKv != nil {
		t.Errorf("PrevKv = %v, want nil", result.PrevKv)
	}
}

func TestProtoToWatchEvent_DeleteEvent(t *testing.T) {
	ev := &pb.Event{
		Type: pb.EventType_EVENT_DELETE,
		Kv: &pb.KeyValue{
			Key:         []byte("deleted-key"),
			ModRevision: 10,
		},
	}
	result := protoToWatchEvent(ev)
	if result == nil {
		t.Fatal("protoToWatchEvent() returned nil for valid input")
	}
	if result.Type != transport.EventDelete {
		t.Errorf("Type = %v, want EventDelete", result.Type)
	}
	if string(result.Kv.Key) != "deleted-key" {
		t.Errorf("Kv.Key = %q, want %q", result.Kv.Key, "deleted-key")
	}
}

func TestProtoToWatchEvent_WithPrevKv(t *testing.T) {
	ev := &pb.Event{
		Type: pb.EventType_EVENT_PUT,
		Kv: &pb.KeyValue{
			Key:   []byte("key1"),
			Value: []byte("new-value"),
		},
		PrevKv: &pb.KeyValue{
			Key:   []byte("key1"),
			Value: []byte("old-value"),
		},
	}
	result := protoToWatchEvent(ev)
	if result == nil {
		t.Fatal("protoToWatchEvent() returned nil for valid input")
	}
	if result.PrevKv == nil {
		t.Fatal("PrevKv is nil, expected non-nil")
	}
	if string(result.PrevKv.Value) != "old-value" {
		t.Errorf("PrevKv.Value = %q, want %q", result.PrevKv.Value, "old-value")
	}
}

// ---------------------------------------------------------------------------
// protoEventTypeToTransport map tests
// ---------------------------------------------------------------------------

func TestProtoEventTypeToTransport_AllValues(t *testing.T) {
	tests := []struct {
		name     string
		input    pb.EventType
		expected transport.EventType
	}{
		{"EVENT_PUT", pb.EventType_EVENT_PUT, transport.EventPut},
		{"EVENT_DELETE", pb.EventType_EVENT_DELETE, transport.EventDelete},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := protoEventTypeToTransport[tt.input]
			if got != tt.expected {
				t.Errorf("protoEventTypeToTransport[%v] = %v, want %v", tt.input, got, tt.expected)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Closed state tests for Watch methods
// ---------------------------------------------------------------------------

func TestTransport_Watch_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	_, err = tr.Watch(ctx, "key", transport.WatchOptions{})
	if err == nil {
		t.Fatal("Watch() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_WatchPrefix_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	_, err = tr.WatchPrefix(ctx, "prefix/", transport.WatchOptions{})
	if err == nil {
		t.Fatal("WatchPrefix() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}
