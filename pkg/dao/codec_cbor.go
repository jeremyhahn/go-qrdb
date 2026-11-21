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

// newCBORCodec builds the CBOR codec for a typed entity with fxamacker's
// decoding defaults: a map held in an any field decodes as map[any]any.
func newCBORCodec[E any]() (codec.Codec[E], error) {
	return cbor.New[E](), nil
}

// newCBORDocumentCodec builds the CBOR codec for generic documents: nested
// maps decode as map[string]any, as a JSON document's do, instead of
// fxamacker's map[any]any default. Typed entities keep the default (see
// newCBORCodec), so an any field may still hold a map with non-string keys.
func newCBORDocumentCodec() (codec.Codec[map[string]any], error) {
	c, err := cbor.NewWithOptions[map[string]any](
		fxcbor.DecOptions{DefaultMapType: reflect.TypeFor[map[string]any]()},
		fxcbor.EncOptions{},
	)
	if err != nil {
		return nil, err
	}
	return c, nil
}
