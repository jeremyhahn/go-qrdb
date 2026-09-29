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

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// capturingHost records the proposed command and answers with result.
func capturingHost(result sm.Result, captured *[]byte) *mockHost {
	return &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, cmd []byte) (sm.Result, error) {
			*captured = append([]byte(nil), cmd...)
			return result, nil
		},
	}
}

func TestIndexService_CreateIndex_CarriesOwner(t *testing.T) {
	var cmd []byte
	svc := NewIndexService(capturingHost(sm.Result{Value: 1}, &cmd), 1)
	if err := svc.CreateIndex(context.Background(), "db.c.f", "f", index.IndexConfig{Owner: "collection:db/c#1"}); err != nil {
		t.Fatalf("CreateIndex: %v", err)
	}
	_, _, opts, err := protocol.DecodeIndexCreateOpts(cmd[1:])
	if err != nil || opts.Owner != "collection:db/c#1" {
		t.Fatalf("the owner must travel on the create command: %+v %v", opts, err)
	}
}

func TestIndexService_DropOwnedIndex_EncodesOwnerCheckedDrop(t *testing.T) {
	var cmd []byte
	svc := NewIndexService(capturingHost(sm.Result{Value: 1}, &cmd), 1)
	if err := svc.DropOwnedIndex(context.Background(), "db.c.f", "collection:db/c#1"); err != nil {
		t.Fatalf("DropOwnedIndex: %v", err)
	}
	name, opts, err := protocol.DecodeIndexDropOpts(cmd[1:])
	if err != nil || name != "db.c.f" || !opts.CheckOwner || opts.Owner != "collection:db/c#1" {
		t.Fatalf("drop command: %q %+v %v", name, opts, err)
	}
}

func TestIndexService_OwnershipRefusalIsTyped(t *testing.T) {
	var cmd []byte
	refused := sm.Result{Data: protocol.EncodeIndexOwnershipError("db.c.f", "")}
	svc := NewIndexService(capturingHost(refused, &cmd), 1)

	err := svc.DropOwnedIndex(context.Background(), "db.c.f", "collection:db/c#1")
	var own *index.OwnershipError
	if !errors.As(err, &own) || own.Name != "db.c.f" || own.Owner != "" || own.Requested != "collection:db/c#1" {
		t.Fatalf("drop refusal: %v", err)
	}
	err = svc.CreateIndex(context.Background(), "db.c.f", "f", index.IndexConfig{Owner: "collection:db/c#1"})
	if !errors.As(err, &own) || own.Requested != "collection:db/c#1" {
		t.Fatalf("create refusal: %v", err)
	}
}

func TestIndexService_DropOwnedIndex_Errors(t *testing.T) {
	sentinel := errors.New("proposal dropped")
	svc := NewIndexService(&mockHost{
		syncProposeFn: func(context.Context, uint64, []byte) (sm.Result, error) { return sm.Result{}, sentinel },
	}, 1)
	var pe *ProposeError
	if err := svc.DropOwnedIndex(context.Background(), "n", "o"); !errors.As(err, &pe) {
		t.Fatalf("expected ProposeError, got %v", err)
	}

	var enc *EncodeRequestError
	if err := svc.DropOwnedIndex(context.Background(), string(make([]byte, 70000)), "o"); !errors.As(err, &enc) {
		t.Fatalf("expected EncodeRequestError, got %v", err)
	}

	notConfigured := NewIndexService(&mockHost{
		syncProposeFn: func(context.Context, uint64, []byte) (sm.Result, error) {
			return sm.Result{Data: protocol.EncodeAppError("index not configured")}, nil
		},
	}, 1)
	var snc *SMNotConfiguredError
	if err := notConfigured.DropOwnedIndex(context.Background(), "n", "o"); !errors.As(err, &snc) {
		t.Fatalf("expected SMNotConfiguredError, got %v", err)
	}
}
