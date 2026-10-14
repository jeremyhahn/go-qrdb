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

package pebbleindex

import (
	"bytes"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/index/indextest"
	"github.com/jeremyhahn/go-qrdb/pkg/index/internal/indexkey"
)

// newConfigured is the indextest.Factory for this package's Index.
func newConfigured(t *testing.T, cfg index.IndexConfig) index.Index {
	t.Helper()
	idx, err := NewWithConfig(openPebble(t), cfg.Name, cfg)
	if err != nil {
		t.Fatalf("NewWithConfig: %v", err)
	}
	return idx
}

func TestIndex_NumericScan(t *testing.T) {
	indextest.NumericScan(t, newConfigured)
}

func TestIndex_NumericExactness(t *testing.T) {
	indextest.NumericExactness(t, newConfigured)
}

// TestNewWithConfig_RefusesStaleEncoding: a numeric index recorded at another
// encoding version is never constructed, so it cannot be served.
func TestNewWithConfig_RefusesStaleEncoding(t *testing.T) {
	stale := index.IndexConfig{Name: "n", ValueType: index.IndexValueTypeNumeric}
	var se *index.StaleEncodingError
	if _, err := NewWithConfig(openPebble(t), "n", stale); !errors.As(err, &se) {
		t.Errorf("NewWithConfig = %v, want *index.StaleEncodingError", err)
	}
}

func TestIndex_StringScan(t *testing.T) {
	indextest.StringScan(t, newConfigured)
}

func TestIndex_CaseFolding(t *testing.T) {
	indextest.CaseFolding(t, newConfigured)
}

// TestNew_IsCaseSensitive is the polarity guard for the constructor that
// takes no config.
func TestNew_IsCaseSensitive(t *testing.T) {
	idx, _ := newPebbleIdx(t, "by_name", false)
	indextest.IsCaseSensitive(t, idx)
}

// TestNewWithConfig_NumericIgnoresCaseSensitivity pins the one interaction
// between the two encodings. A numeric index parses its values as numbers and
// encodes them to binary terms in which case has no meaning; folding a term
// would rewrite its bytes and break its order.
func TestNewWithConfig_NumericIgnoresCaseSensitivity(t *testing.T) {
	idx, err := NewWithConfig(openPebble(t), "by_age", index.IndexConfig{
		Name:            "by_age",
		ValueType:       index.IndexValueTypeNumeric,
		EncodingVersion: index.NumericEncodingVersion,
		// Deliberately the folding polarity.
		CaseSensitive: false,
	})
	if err != nil {
		t.Fatalf("NewWithConfig: %v", err)
	}
	if idx.caseInsensitive {
		t.Error("a numeric index was built case-insensitive; folding has no meaning " +
			"over numeric terms and would corrupt them")
	}

	if err := idx.Put([]byte("user:1"), []byte("-42")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	keys, err := idx.Get([]byte("-42"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(keys) != 1 {
		t.Errorf("Get(-42) = %d keys; want 1", len(keys))
	}
}

// TestIndex_ResolveScanBound_NonNumericPassesThrough: a string index takes
// its bound bytes, exclusivity marker included, verbatim.
func TestIndex_ResolveScanBound_NonNumericPassesThrough(t *testing.T) {
	idx, _ := newPebbleIdx(t, "s", false)
	got, err := idx.resolveScanBound([]byte("hello\x00"), "scan start")
	if err != nil {
		t.Fatalf("resolveScanBound: unexpected error: %v", err)
	}
	if string(got) != "hello\x00" {
		t.Fatalf("resolveScanBound: got %q, want %q", got, "hello\x00")
	}
}

// TestIndex_ResolveScanBound_NumericSuccessor: a numeric index resolves an
// exclusive bound to the successor of the encoded value.
func TestIndex_ResolveScanBound_NumericSuccessor(t *testing.T) {
	idx, err := New(openPebble(t), "n", false, index.IndexValueTypeNumeric)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	base, err := index.EncodeNumericValue("5")
	if err != nil {
		t.Fatalf("EncodeNumericValue: %v", err)
	}
	want := indexkey.Successor(base)

	got, err := idx.resolveScanBound(indextest.AppendExclusiveSuffix([]byte("5")), "scan start")
	if err != nil {
		t.Fatalf("resolveScanBound: unexpected error: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("resolveScanBound: got % x, want % x (successor of encoded 5)", got, want)
	}
}
