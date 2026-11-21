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

package dao

import (
	"reflect"

	fxcbor "github.com/fxamacker/cbor/v2"
	"github.com/jeremyhahn/go-codec"
	"github.com/jeremyhahn/go-codec/pkg/cbor"
)

// newCBORCodec builds the CBOR codec with string-keyed maps as the default
// map type, so a nested object in a document decodes as map[string]any
// (fxamacker's default is map[interface{}]interface{}, which JSON documents
// and the query layer do not handle).
func newCBORCodec[E any]() (codec.Codec[E], error) {
	c, err := cbor.NewWithOptions[E](
		fxcbor.DecOptions{DefaultMapType: reflect.TypeFor[map[string]any]()},
		fxcbor.EncOptions{},
	)
	if err != nil {
		return nil, err
	}
	return c, nil
}
