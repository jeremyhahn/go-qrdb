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

// coverage_gaps22_test.go covers the final 3 statements for ≥91%.
package service

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
)

// ---------------------------------------------------------------------------
// A KVServicer that fails on Put
// ---------------------------------------------------------------------------

type putAlwaysFailKV struct {
	*memoryKVSvc
	putErr error
}

func newPutAlwaysFailKV() *putAlwaysFailKV {
	return &putAlwaysFailKV{
		memoryKVSvc: newMemoryKVSvc(),
		putErr:      errors.New("put always fails"),
	}
}

func (k *putAlwaysFailKV) Put(_ context.Context, _ string, _ []byte) error {
	return k.putErr
}

// ---------------------------------------------------------------------------
// collection_service.go – CreateCollection: Put metadata fails (line 190-192)
// ---------------------------------------------------------------------------

func TestCollectionService_CreateCollection_MetadataSaveFails(t *testing.T) {
	svc := NewCollectionService(newPutAlwaysFailKV())
	ctx := context.Background()

	_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name:     "fail-col",
		Database: "mydb",
	})
	if err == nil {
		t.Fatal("expected error when metadata save fails, got nil")
	}
}

// ---------------------------------------------------------------------------
// collection_service.go – CreateCollection: index creation fails (line 203-205)
// ---------------------------------------------------------------------------

type failingIndexSvc struct {
	createErr error
}

func (f *failingIndexSvc) CreateIndex(_ context.Context, _, _ string, _ index.IndexConfig) error {
	return f.createErr
}

func (f *failingIndexSvc) DropIndex(_ context.Context, _ string) error {
	return nil
}

func TestCollectionService_CreateCollection_IndexCreationFails(t *testing.T) {
	kv := newMemoryKVSvc()
	idxSvc := &failingIndexSvc{createErr: errors.New("index creation failed")}
	svc := NewCollectionServiceWithIndex(kv, idxSvc)
	ctx := context.Background()

	_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name:     "idx-fail-col",
		Database: "mydb",
		Indexes: []IndexDefinition{
			{FieldName: "email", Unique: true},
		},
	})
	if err == nil {
		t.Fatal("expected error when index creation fails, got nil")
	}
}

// ---------------------------------------------------------------------------
// One more: authfactor_service.go:236-238 loadEnrollment kv.Get empty bytes
// (this is line 236 where len(data) == 0)
// ---------------------------------------------------------------------------

func TestAuthFactorService_LoadEnrollment_KVGetEmpty_v2(t *testing.T) {
	kv := newAFMockKV()
	enrollID := "zero-bytes-enrollment"
	// Store empty bytes.
	_ = kv.Put(context.Background(), sysEnrollmentPrefix+enrollID, []byte{})

	svc, err := NewAuthFactorService(AuthFactorServiceConfig{
		Factors: nil,
		KV:      kv,
	})
	if err != nil {
		t.Fatalf("NewAuthFactorService: %v", err)
	}

	// ChallengeFactor → loadEnrollment → len(data) == 0 → EnrollmentNotFoundError.
	_, challengeErr := svc.ChallengeFactor(context.Background(), enrollID)
	if challengeErr == nil {
		t.Fatal("expected error for zero-byte enrollment data, got nil")
	}
}
