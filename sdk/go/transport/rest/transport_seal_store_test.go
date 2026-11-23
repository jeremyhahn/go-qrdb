// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package rest

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestSealStore_EmbeddedOnlyRefusal checks every embedded-only seal-store
// verb is refused with *transport.EmbeddedOnlyError on a Transport that is
// never dialled: no network I/O, the verbs are not served (ruling 1234).
func TestSealStore_EmbeddedOnlyRefusal(t *testing.T) {
	ctx := context.Background()
	tr := &Transport{}
	calls := map[string]func() error{
		transport.OpSealStorePut: func() error {
			return tr.SealStorePut(ctx, &transport.SealStorePutRequest{Name: "alpha", Secret: []byte("s")})
		},
		transport.OpSealStoreGet: func() error {
			resp, err := tr.SealStoreGet(ctx, &transport.SealStoreGetRequest{Name: "alpha"})
			if resp != nil {
				t.Errorf("SealStoreGet response = %+v; want nil", resp)
			}
			return err
		},
		transport.OpSealStoreDelete: func() error {
			return tr.SealStoreDelete(ctx, &transport.SealStoreDeleteRequest{Name: "alpha"})
		},
		transport.OpSealStoreList: func() error {
			resp, err := tr.SealStoreList(ctx)
			if resp != nil {
				t.Errorf("SealStoreList response = %+v; want nil", resp)
			}
			return err
		},
		transport.OpSealStoreReseal: func() error {
			return tr.SealStoreReseal(ctx, &transport.SealStoreResealRequest{Name: "alpha"})
		},
		transport.OpSealStoreStatus: func() error {
			resp, err := tr.SealStoreStatus(ctx)
			if resp != nil {
				t.Errorf("SealStoreStatus response = %+v; want nil", resp)
			}
			return err
		},
	}
	if len(calls) != len(transport.EmbeddedOnlyOperations) {
		t.Fatalf("table covers %d verbs; EmbeddedOnlyOperations lists %d", len(calls), len(transport.EmbeddedOnlyOperations))
	}
	for _, op := range transport.EmbeddedOnlyOperations {
		t.Run(op, func(t *testing.T) {
			call, ok := calls[op]
			if !ok {
				t.Fatalf("no call for %s", op)
			}
			err := call()
			if !errors.Is(err, transport.ErrEmbeddedOnly) {
				t.Fatalf("%s error = %v; want transport.ErrEmbeddedOnly", op, err)
			}
			var eo *transport.EmbeddedOnlyError
			if !errors.As(err, &eo) || eo.Operation != op || eo.Transport != "rest" {
				t.Fatalf("%s error = %#v; want *EmbeddedOnlyError{Operation: %q, Transport: %q}", op, err, op, "rest")
			}
		})
	}
}
