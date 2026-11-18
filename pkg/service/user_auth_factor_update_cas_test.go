// Copyright (c) 2026 Jeremy Hahn
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

package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore/kvstoretest"
)

// interposingStore runs before once, just ahead of the first guarded batch
// that writes a factor primary: the point where a writer has read the record
// and decided its write, but the write has not landed.
type interposingStore struct {
	*kvstoretest.GuardedMemoryStore
	before func()
}

func (s *interposingStore) PutAllIfAllUnchanged(ctx context.Context, guards []kvstore.Guard, writes []kvstore.KV) (bool, error) {
	if s.before != nil {
		for _, w := range writes {
			if !w.Delete && strings.HasPrefix(w.Key, userFactorPrimaryPrefix) {
				before := s.before
				s.before = nil
				before()
				break
			}
		}
	}
	return s.GuardedMemoryStore.PutAllIfAllUnchanged(ctx, guards, writes)
}

// A Relabel or MarkUsed that read the factor before a Purge -- run here by a
// second service over the same store, as another node would, so the service
// mutex does not order them -- fails its guard after the Purge, and its retry
// finds the factor gone. The purged credential is never recreated.
func TestFactorUpdate_ReadBeforePurge_CannotRecreatePurgedFactor(t *testing.T) {
	cases := map[string]func(UserAuthFactorServicer, context.Context) error{
		"Relabel": func(svc UserAuthFactorServicer, ctx context.Context) error {
			return svc.Relabel(ctx, "f1", "renamed")
		},
		"MarkUsed": func(svc UserAuthFactorServicer, ctx context.Context) error {
			return svc.MarkUsed(ctx, "f1", time.Now().UTC())
		},
	}
	for name, update := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			kv := &interposingStore{GuardedMemoryStore: kvstoretest.NewGuardedMemoryStore()}
			newSvc := func() UserAuthFactorServicer {
				svc, err := NewUserAuthFactorService(UserAuthFactorServiceConfig{
					KV:               kv,
					UserStateUpdater: newStubUserStateUpdater(),
				})
				if err != nil {
					t.Fatalf("NewUserAuthFactorService: %v", err)
				}
				return svc
			}
			writer, purgingNode := newSvc(), newSvc()
			if err := writer.Add(ctx, 1, mtlsFactor("f1", 1, []byte("k1"))); err != nil {
				t.Fatalf("Add: %v", err)
			}

			purged := -1
			kv.before = func() {
				n, err := purgingNode.(UserAuthFactorPurger).Purge(ctx, 1)
				if err != nil {
					t.Errorf("Purge: %v", err)
				}
				purged = n
			}
			err := update(writer, ctx)
			if purged != 1 {
				t.Fatalf("Purge under the update removed %d factors, want 1", purged)
			}
			if !errors.Is(err, auth.ErrFactorNotFound) {
				t.Fatalf("%s after Purge = %v, want ErrFactorNotFound", name, err)
			}
			assertNoPrimary(t, kv, "f1")
			if _, err := writer.LookupByCredentialKey(ctx, auth.AuthFactorMTLS, []byte("k1")); !errors.Is(err, auth.ErrFactorNotFound) {
				t.Fatalf("credential after the refused %s = %v, want ErrFactorNotFound", name, err)
			}
		})
	}
}

// A sealed set refuses Relabel and MarkUsed outright, even with the factor's
// primary still in place, as a part-way Purge leaves it.
func TestFactorUpdate_RefusesSealedSet(t *testing.T) {
	ctx := context.Background()
	svc, _ := newPurger(t)
	if err := svc.Add(ctx, 1, mtlsFactor("f1", 1, []byte("k1"))); err != nil {
		t.Fatalf("Add: %v", err)
	}
	store := svc.(*userAuthFactorService)
	if _, err := store.commitFactorSet(ctx, store.atomicStore(), 1, func(set *factorSet) error {
		set.Sealed = true
		return nil
	}); err != nil {
		t.Fatalf("seal: %v", err)
	}
	var sealed *FactorSetSealedError
	if err := svc.Relabel(ctx, "f1", "renamed"); !errors.As(err, &sealed) {
		t.Fatalf("Relabel of a sealed set = %v, want FactorSetSealedError", err)
	}
	if err := svc.MarkUsed(ctx, "f1", time.Now().UTC()); !errors.As(err, &sealed) {
		t.Fatalf("MarkUsed of a sealed set = %v, want FactorSetSealedError", err)
	}
	got, err := svc.Get(ctx, "f1")
	if err != nil || got.Label == "renamed" || got.LastUsedAt != nil {
		t.Fatalf("factor after the refused updates = %+v, %v; want unchanged", got, err)
	}
}
