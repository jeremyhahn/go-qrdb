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

//go:build codec_cbor

package service

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/dao"
)

// A CBOR collection accepts no unique index: the engine could not decode its
// documents, so two documents could share a uniquely indexed value.
func TestCollectionService_CreateCollection_IndexesRequireJSONCodec_CBOR(t *testing.T) {
	assertIndexesRequireJSON(t, dao.CodecCBOR, false)
	assertIndexesRequireJSON(t, dao.CodecCBOR, true)
}
