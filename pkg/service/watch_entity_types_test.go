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

import "testing"

func TestWatchEntityRequest_FieldAssignment(t *testing.T) {
	req := WatchEntityRequest{
		EntityType:    "user",
		ID:            "u1",
		ShardID:       5,
		StartRevision: 100,
	}
	if req.EntityType != "user" {
		t.Fatalf("expected EntityType 'user', got %q", req.EntityType)
	}
	if req.ID != "u1" {
		t.Fatalf("expected ID 'u1', got %q", req.ID)
	}
	if req.ShardID != 5 {
		t.Fatalf("expected ShardID 5, got %d", req.ShardID)
	}
	if req.StartRevision != 100 {
		t.Fatalf("expected StartRevision 100, got %d", req.StartRevision)
	}
}

func TestWatchEntityRequest_ZeroValues(t *testing.T) {
	req := WatchEntityRequest{}
	if req.EntityType != "" {
		t.Fatalf("expected empty EntityType, got %q", req.EntityType)
	}
	if req.StartRevision != 0 {
		t.Fatalf("expected StartRevision 0, got %d", req.StartRevision)
	}
}

func TestWatchEntityTypeRequest_FieldAssignment(t *testing.T) {
	req := WatchEntityTypeRequest{
		EntityType:    "config",
		ShardID:       3,
		StartRevision: 50,
	}
	if req.EntityType != "config" {
		t.Fatalf("expected EntityType 'config', got %q", req.EntityType)
	}
	if req.ShardID != 3 {
		t.Fatalf("expected ShardID 3, got %d", req.ShardID)
	}
	if req.StartRevision != 50 {
		t.Fatalf("expected StartRevision 50, got %d", req.StartRevision)
	}
}

func TestWatchEntityTypeRequest_ZeroValues(t *testing.T) {
	req := WatchEntityTypeRequest{}
	if req.EntityType != "" {
		t.Fatalf("expected empty EntityType, got %q", req.EntityType)
	}
}

func TestWatchEntityEvent_PutEvent(t *testing.T) {
	evt := WatchEntityEvent{
		EntityType: "user",
		ID:         "u1",
		Data:       []byte("alice"),
		Revision:   42,
		Type:       WatchEventPut,
	}
	if evt.EntityType != "user" {
		t.Fatalf("expected EntityType 'user', got %q", evt.EntityType)
	}
	if evt.ID != "u1" {
		t.Fatalf("expected ID 'u1', got %q", evt.ID)
	}
	if string(evt.Data) != "alice" {
		t.Fatalf("expected Data 'alice', got %q", evt.Data)
	}
	if evt.Revision != 42 {
		t.Fatalf("expected Revision 42, got %d", evt.Revision)
	}
	if evt.Type != WatchEventPut {
		t.Fatalf("expected WatchEventPut, got %d", evt.Type)
	}
}

func TestWatchEntityEvent_DeleteEvent(t *testing.T) {
	evt := WatchEntityEvent{
		EntityType: "session",
		ID:         "s1",
		Data:       nil,
		Revision:   99,
		Type:       WatchEventDelete,
	}
	if evt.Type != WatchEventDelete {
		t.Fatalf("expected WatchEventDelete, got %d", evt.Type)
	}
	if evt.Data != nil {
		t.Fatalf("expected nil Data for delete, got %v", evt.Data)
	}
}

func TestWatchEventType_Values(t *testing.T) {
	if WatchEventPut != 0 {
		t.Fatalf("expected WatchEventPut=0, got %d", WatchEventPut)
	}
	if WatchEventDelete != 1 {
		t.Fatalf("expected WatchEventDelete=1, got %d", WatchEventDelete)
	}
}
