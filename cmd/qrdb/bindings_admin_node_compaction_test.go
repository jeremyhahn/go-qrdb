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

package main

import (
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestAdminBinding_NodeCompaction_SetReadAndClear(t *testing.T) {
	svc := &nodeCompactionAdminService{}
	b := NewAdminBinding(testLogger(t), newTestConnectionBinding(t, newMockKVService(), svc))

	if got, err := b.NodeCompaction(); err != nil || got != nil {
		t.Fatalf("NodeCompaction before any policy = %+v, %v; want none", got, err)
	}
	set, err := b.SetNodeCompaction(&transport.DatabaseCompactionConfig{Mode: "revision", RetainRevisions: 8})
	if err != nil || set == nil || set.RetainRevisions != 8 {
		t.Fatalf("SetNodeCompaction = %+v, %v; want the revision policy", set, err)
	}
	if got, err := b.NodeCompaction(); err != nil || got == nil || got.Mode != "revision" {
		t.Fatalf("NodeCompaction = %+v, %v; want the policy set", got, err)
	}
	if off, err := b.SetNodeCompaction(nil); err != nil || off != nil || svc.policy != nil {
		t.Fatalf("SetNodeCompaction(nil) = %+v, %v (held %+v); want off", off, err, svc.policy)
	}
}

func TestAdminBinding_NodeCompaction_Failures(t *testing.T) {
	unconnected := NewAdminBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	var nce *NotConnectedError
	if _, err := unconnected.NodeCompaction(); !errors.As(err, &nce) {
		t.Fatalf("NodeCompaction unconnected = %v; want *NotConnectedError", err)
	}
	if _, err := unconnected.SetNodeCompaction(nil); !errors.As(err, &nce) {
		t.Fatalf("SetNodeCompaction unconnected = %v; want *NotConnectedError", err)
	}

	down := NewAdminBinding(testLogger(t), newTestConnectionBinding(t, newMockKVService(),
		&nodeCompactionAdminService{err: &service.NodeCompactionUnavailableError{}}))
	if _, err := down.NodeCompaction(); err == nil {
		t.Fatal("NodeCompaction on a node without the table succeeded")
	}
	if _, err := down.SetNodeCompaction(nil); err == nil {
		t.Fatal("SetNodeCompaction on a node without the table succeeded")
	}
}
