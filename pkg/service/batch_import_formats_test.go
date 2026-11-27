// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package service

import (
	"reflect"
	"testing"
)

func TestImportFormats_List(t *testing.T) {
	want := []string{"auto", "csv", "json", "ndjson", "yaml", "archive"}
	if got := ImportFormats(); !reflect.DeepEqual(got, want) {
		t.Fatalf("ImportFormats() = %v, want %v", got, want)
	}
}

func TestImportFormats_ReturnsCopy(t *testing.T) {
	got := ImportFormats()
	got[0] = "mutated"
	if ImportFormats()[0] != "auto" {
		t.Fatal("ImportFormats returned the package slice, not a copy")
	}
}

// TestImportFormats_AcceptedByImport pins the advertised list to the
// import's own format switch: a format the list reports must not be
// refused as an invalid format.
func TestImportFormats_AcceptedByImport(t *testing.T) {
	for _, format := range ImportFormats() {
		opts := ImportOptions{Format: format, KeyField: "id", ValueMode: "row"}
		if err := validateImportOptions(opts); err != nil {
			t.Errorf("validateImportOptions(Format=%q) = %v, want nil", format, err)
		}
	}
}
