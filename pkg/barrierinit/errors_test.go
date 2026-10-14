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

package barrierinit

import (
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
)

// Every message names its origin and carries the fields an operator needs to
// find the record or statement it is about.
func TestErrors_MessagesNameTheirSubject(t *testing.T) {
	barrier := clusterseal.TenantBarrier("acme")
	cause := errors.New("cause")
	cases := map[string]struct {
		err  error
		want []string
	}{
		"statement":               {&InitStatementError{Barrier: barrier, Epoch: 3, Reason: ReasonWrongSigner}, []string{string(barrier), "epoch 3", string(ReasonWrongSigner)}},
		"statement with cause":    {&InitStatementError{Barrier: barrier, Epoch: 1, Reason: ReasonWrongSigner, Err: cause}, []string{string(ReasonWrongSigner), "cause"}},
		"already initialised":     {&BarrierAlreadyInitializedError{Barrier: barrier}, []string{string(barrier), "already initialised"}},
		"epoch conflict":          {&EpochConflictError{Barrier: barrier, Epoch: 4, Current: 2}, []string{"at epoch 2", "epoch 4"}},
		"corrupt with reason":     {&CorruptRecordError{Key: "k", Reason: "no versions"}, []string{"record k", "no versions"}},
		"corrupt with cause":      {&CorruptRecordError{Key: "k", Err: cause}, []string{"record k", "cause"}},
		"guarded writes":          {&GuardedWritesUnavailableError{}, []string{"guarded writes"}},
		"config":                  {&ConfigError{Field: "Book"}, []string{"Book is required"}},
		"store":                   {&StoreError{Op: "read init record", Err: cause}, []string{"read init record", "cause"}},
		"statement without cause": {&InitStatementError{Barrier: barrier, Reason: ReasonWrongSigner}, []string{string(ReasonWrongSigner)}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			msg := tc.err.Error()
			if !strings.HasPrefix(msg, "barrierinit: ") {
				t.Fatalf("%q does not name its origin", msg)
			}
			for _, want := range tc.want {
				if !strings.Contains(msg, want) {
					t.Fatalf("%q does not mention %q", msg, want)
				}
			}
		})
	}
}

func TestErrors_UnwrapToTheirCause(t *testing.T) {
	cause := errors.New("cause")
	for name, err := range map[string]error{
		"statement": &InitStatementError{Reason: ReasonWrongSigner, Err: cause},
		"corrupt":   &CorruptRecordError{Key: "k", Err: cause},
		"store":     &StoreError{Op: "read init record", Err: cause},
	} {
		if !errors.Is(err, cause) {
			t.Fatalf("%s: errors.Is(%v, cause) = false", name, err)
		}
	}
	if errors.Unwrap(&CorruptRecordError{Key: "k", Reason: "no versions"}) != nil {
		t.Fatal("a corrupt record without a decode failure unwraps to a cause")
	}
}
