//go:build !integration

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
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestCollectionCreate_IndexFlag covers the CLI half of go-qrdb#414: repeated
// --index flags become index definitions on the request, and the created
// collection's state and indexes are printed.
func TestCollectionCreate_IndexFlag(t *testing.T) {
	var captured *service.CreateCollectionRequest
	svc := &mockCollectionService{
		createCollectionFn: func(_ context.Context, req *service.CreateCollectionRequest) (*service.CollectionInfo, error) {
			captured = req
			return &service.CollectionInfo{
				Name: req.Name, Database: req.Database, Indexes: req.Indexes,
				State: service.CollectionStateActive, CreatedAt: time.Now(),
			}, nil
		},
	}

	output, err := executeCollectionCmd(t, svc, "create", "users", "--database", "mydb",
		"--index", "email,unique", "--index", "age, SPARSE", "--index", "name")
	if err != nil {
		t.Fatalf("collection create error: %v", err)
	}
	want := []service.IndexDefinition{{FieldName: "email", Unique: true}, {FieldName: "age", Sparse: true}, {FieldName: "name"}}
	if captured == nil || !reflect.DeepEqual(captured.Indexes, want) {
		t.Fatalf("captured = %+v", captured)
	}
	for _, line := range []string{"State:         active", "email,unique", "age,sparse"} {
		if !strings.Contains(output, line) {
			t.Errorf("output missing %q:\n%s", line, output)
		}
	}
}

func TestParseCollectionIndexFlags_Refusals(t *testing.T) {
	for name, spec := range map[string]string{"empty field": ",unique", "unknown option": "email,primary"} {
		_, err := parseCollectionIndexFlags([]string{spec})
		var bad *InvalidArgError
		if !errors.As(err, &bad) {
			t.Errorf("%s: expected InvalidArgError, got %v", name, err)
		}
	}
	if defs, err := parseCollectionIndexFlags(nil); defs != nil || err != nil {
		t.Fatalf("no flags must mean no definitions: %v %v", defs, err)
	}
}

func TestCollectionStateLabel(t *testing.T) {
	if got := collectionStateLabel(&transport.CollectionInfo{State: "active", StateSince: "x"}); got != "active" {
		t.Errorf("active: %q", got)
	}
	if got := collectionStateLabel(&transport.CollectionInfo{State: "dropping", StateSince: "2026-09-25T12:00:00Z"}); got != "dropping (since 2026-09-25T12:00:00Z)" {
		t.Errorf("dropping: %q", got)
	}
	if got := collectionStateLabel(&transport.CollectionInfo{State: "creating"}); got != "creating" {
		t.Errorf("creating without a time: %q", got)
	}
}
