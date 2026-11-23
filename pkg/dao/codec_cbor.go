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
	"fmt"
	"io"
	"math"
	"reflect"

	fxcbor "github.com/fxamacker/cbor/v2"
	"github.com/jeremyhahn/go-codec"
	"github.com/jeremyhahn/go-codec/pkg/cbor"
)

// CBOR structural limits, one set for encode and decode. fxamacker's
// defaults (32 levels, 131072 array elements and map pairs) are tighter than
// what the encoder writes and what the JSON codec accepts, so a document
// could be stored and then fail to decode. The limits match the JSON codec:
// encoding/json refuses nesting deeper than 10000 and has no element or pair
// limit, so the element and pair limits are the CBOR library's maximum.
const (
	// CBORMaxNestedLevels is the deepest nesting of arrays, maps and tags
	// a CBOR value may have.
	CBORMaxNestedLevels = 10000
	// CBORMaxArrayElements is the largest number of elements in one array.
	CBORMaxArrayElements = math.MaxInt32
	// CBORMaxMapPairs is the largest number of key-value pairs in one map.
	CBORMaxMapPairs = math.MaxInt32
)

// CBORLimitError reports a value the CBOR codec refuses to encode because
// its encoding exceeds the structural limits the decoder enforces.
type CBORLimitError struct {
	Cause error
}

func (e CBORLimitError) Error() string {
	return fmt.Sprintf("cbor: value exceeds the codec limits (max nested levels %d, max array elements %d, max map pairs %d): %v",
		CBORMaxNestedLevels, CBORMaxArrayElements, CBORMaxMapPairs, e.Cause)
}

func (e CBORLimitError) Unwrap() error { return e.Cause }

// CBORDecodeShapeError reports a value the CBOR codec refuses to encode
// because its encoding cannot be decoded back as the stored type, such as a
// map with non-string keys inside a generic document.
type CBORDecodeShapeError struct {
	Cause error
}

func (e CBORDecodeShapeError) Error() string {
	return fmt.Sprintf("cbor: value encodes but cannot be decoded back as the stored type (e.g. a map with non-string keys inside a document): %v", e.Cause)
}

func (e CBORDecodeShapeError) Unwrap() error { return e.Cause }

// cborDecOptions returns the decode options with the shared limits applied.
func cborDecOptions(defaultMapType reflect.Type) fxcbor.DecOptions {
	return fxcbor.DecOptions{
		MaxNestedLevels:  CBORMaxNestedLevels,
		MaxArrayElements: CBORMaxArrayElements,
		MaxMapPairs:      CBORMaxMapPairs,
		DefaultMapType:   defaultMapType,
	}
}

// limitedCBORCodec checks every encoding against the decoder's limits and
// decodes it back once as E with the same DecMode before returning it, so
// nothing is written that cannot be read back.
type limitedCBORCodec[E any] struct {
	inner codec.Codec[E]
	dm    fxcbor.DecMode
}

func newLimitedCBORCodec[E any](dec fxcbor.DecOptions) (codec.Codec[E], error) {
	inner, err := cbor.NewWithOptions[E](dec, fxcbor.EncOptions{})
	if err != nil {
		return nil, err
	}
	dm, err := dec.DecMode()
	if err != nil {
		return nil, err
	}
	return &limitedCBORCodec[E]{inner: inner, dm: dm}, nil
}

// Marshal encodes data and refuses an encoding past the limits.
func (c *limitedCBORCodec[E]) Marshal(data E) ([]byte, error) {
	b, err := c.inner.Marshal(data)
	if err != nil {
		return nil, err
	}
	if err := c.dm.Wellformed(b); err != nil {
		return nil, CBORLimitError{Cause: err}
	}
	var probe E
	if err := c.dm.Unmarshal(b, &probe); err != nil {
		return nil, CBORDecodeShapeError{Cause: err}
	}
	return b, nil
}

// Unmarshal decodes data under the same limits.
func (c *limitedCBORCodec[E]) Unmarshal(data []byte, v *E) error {
	return c.inner.Unmarshal(data, v)
}

// Encode writes the checked encoding of data to w.
func (c *limitedCBORCodec[E]) Encode(w io.Writer, data E) error {
	b, err := c.Marshal(data)
	if err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

// Decode reads one value from r under the same limits.
func (c *limitedCBORCodec[E]) Decode(r io.Reader, v *E) error {
	return c.inner.Decode(r, v)
}

// newCBORCodec builds the CBOR codec for a typed entity with fxamacker's
// default map type and the shared structural limits.
func newCBORCodec[E any]() (codec.Codec[E], error) {
	return newLimitedCBORCodec[E](cborDecOptions(nil))
}

// newCBORDocumentCodec builds the CBOR codec for generic documents: nested
// objects decode as map[string]any, the shape the JSON codec produces (the
// default map[interface{}]interface{} is kept for typed entities, see
// newCBORCodec), so an any field may still hold a map with non-string keys.
// It shares the structural limits of newCBORCodec.
func newCBORDocumentCodec() (codec.Codec[map[string]any], error) {
	return newLimitedCBORCodec[map[string]any](cborDecOptions(reflect.TypeFor[map[string]any]()))
}
