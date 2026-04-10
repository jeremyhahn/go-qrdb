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

package planner_test

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/planner"
)

func TestStaticCatalogAvailableIndexes(t *testing.T) {
	infos := []planner.IndexInfo{
		{Name: "mydb.users.Email", FieldName: "Email", Unique: true, FieldType: "string"},
		{Name: "mydb.users.Age", FieldName: "Age", Unique: false, FieldType: "uint64"},
	}

	cat := planner.NewStaticCatalog(infos)
	got := cat.AvailableIndexes()

	if len(got) != 2 {
		t.Fatalf("expected 2 indexes, got %d", len(got))
	}
	if got[0].Name != "mydb.users.Email" {
		t.Errorf("unexpected first index name: %s", got[0].Name)
	}
	if got[1].FieldName != "Age" {
		t.Errorf("unexpected second field name: %s", got[1].FieldName)
	}
	if !got[0].Unique {
		t.Error("expected first index to be unique")
	}
	if got[1].Unique {
		t.Error("expected second index to be non-unique")
	}
}

func TestStaticCatalogReturnsCopy(t *testing.T) {
	infos := []planner.IndexInfo{
		{Name: "mydb.users.Email", FieldName: "Email", Unique: true, FieldType: "string"},
	}
	cat := planner.NewStaticCatalog(infos)

	// Mutate original slice after construction.
	infos[0].Name = "mutated"

	got := cat.AvailableIndexes()
	if got[0].Name == "mutated" {
		t.Error("catalog should be independent of the original slice")
	}

	// Mutate returned slice.
	got[0].Name = "also-mutated"
	got2 := cat.AvailableIndexes()
	if got2[0].Name == "also-mutated" {
		t.Error("AvailableIndexes should return a copy each time")
	}
}

func TestEmptyCatalog(t *testing.T) {
	cat := planner.NewStaticCatalog(nil)
	got := cat.AvailableIndexes()

	if len(got) != 0 {
		t.Errorf("expected 0 indexes for nil input, got %d", len(got))
	}

	cat2 := planner.NewStaticCatalog([]planner.IndexInfo{})
	got2 := cat2.AvailableIndexes()
	if len(got2) != 0 {
		t.Errorf("expected 0 indexes for empty slice, got %d", len(got2))
	}
}

func TestStaticCatalogImplementsIndexCatalog(t *testing.T) {
	// Compile-time interface satisfaction check via assignment.
	var _ planner.IndexCatalog = planner.NewStaticCatalog(nil)
}

func TestIndexInfoFields(t *testing.T) {
	info := planner.IndexInfo{
		Name:      "mydb.orders.Total",
		FieldName: "Total",
		Unique:    false,
		FieldType: "float64",
	}

	if info.Name != "mydb.orders.Total" {
		t.Errorf("unexpected Name: %s", info.Name)
	}
	if info.FieldName != "Total" {
		t.Errorf("unexpected FieldName: %s", info.FieldName)
	}
	if info.Unique {
		t.Error("expected Unique=false")
	}
	if info.FieldType != "float64" {
		t.Errorf("unexpected FieldType: %s", info.FieldType)
	}
}
