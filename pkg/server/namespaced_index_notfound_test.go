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

package server

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// missingIndexService answers every query and scan the way IndexService does
// for an index the state machine does not have: *service.IndexNotFoundError
// carrying the name it was asked for, which under the wrapper is prefixed.
type missingIndexService struct {
	service.IndexServicer
}

func (m *missingIndexService) Query(_ context.Context, indexName string, _ []byte) ([][]byte, error) {
	return nil, &service.IndexNotFoundError{Name: indexName}
}

func (m *missingIndexService) Scan(_ context.Context, indexName string, _, _ []byte) (map[string][][]byte, error) {
	return nil, &service.IndexNotFoundError{Name: indexName}
}

// TestNamespacedIndex_MissingIndexKeepsCallerName pins that a missing index
// stays *service.IndexNotFoundError through the namespace wrapper and names
// the index the caller asked for, without the internal namespace prefix.
func TestNamespacedIndex_MissingIndexKeepsCallerName(t *testing.T) {
	const prefix = "acme/sales/"
	w := newNamespacedIndexService(&missingIndexService{}, prefix)

	calls := map[string]func() error{
		"query": func() error {
			_, err := w.Query(context.Background(), "users.email", []byte("v"))
			return err
		},
		"scan": func() error {
			_, err := w.Scan(context.Background(), "users.email", nil, nil)
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			err := call()
			var inf *service.IndexNotFoundError
			if !errors.As(err, &inf) {
				t.Fatalf("expected *service.IndexNotFoundError, got %T: %v", err, err)
			}
			if inf.Name != "users.email" {
				t.Fatalf("Name = %q, want the caller's %q", inf.Name, "users.email")
			}
			if strings.Contains(err.Error(), prefix) {
				t.Fatalf("error leaks the namespace prefix: %v", err)
			}
		})
	}
}

// TestNamespacedIndex_UnscopeNotFoundPassesOtherErrors pins that an error
// other than a missing index is returned unchanged.
func TestNamespacedIndex_UnscopeNotFoundPassesOtherErrors(t *testing.T) {
	w := newNamespacedIndexService(&missingIndexService{}, "acme/sales/")
	other := &service.ReadError{Err: errors.New("boom")}
	if got := w.unscopeNotFound(other, "users.email"); got != other {
		t.Fatalf("unscopeNotFound changed an unrelated error: %v", got)
	}
}
