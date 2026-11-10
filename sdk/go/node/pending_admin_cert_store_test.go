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

package node

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

func TestPendingAdminCertStore_DeleteRemovesTheRecord(t *testing.T) {
	n := newBareStandaloneNode(t)
	store := PendingAdminCertStore(n.Services().KV)
	ctx := context.Background()
	if err := store.Put(ctx, "admin", []byte("pem")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got, err := store.Get(ctx, "admin"); err != nil || string(got) != "pem" {
		t.Fatalf("Get = %q, %v", got, err)
	}
	if err := store.Delete(ctx, "admin"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	var notFound *service.KeyNotFoundError
	if got, err := store.Get(ctx, "admin"); !errors.As(err, &notFound) {
		t.Fatalf("Get after Delete = %q, %v; want *service.KeyNotFoundError", got, err)
	}
}
