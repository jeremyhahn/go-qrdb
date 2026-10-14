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

package index_test

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/index/indextest"
)

// TestMemoryIndex_CaseFolding runs the case-sensitivity contract against the
// in-memory index MVCCEngine.handleIndexCreate can choose.
func TestMemoryIndex_CaseFolding(t *testing.T) {
	indextest.CaseFolding(t, func(_ *testing.T, cfg index.IndexConfig) index.Index {
		return index.NewMemoryIndexWithConfig(cfg.Name, cfg)
	})
}

// TestNewMemoryIndex_IsCaseSensitive is the polarity guard for the
// constructor that takes no config.
func TestNewMemoryIndex_IsCaseSensitive(t *testing.T) {
	indextest.IsCaseSensitive(t, index.NewMemoryIndex("by_name", false))
}
