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

package dao

import (
	"reflect"

	"github.com/jeremyhahn/go-codec"
)

// bsonDocumentCodec wraps the go-codec BSON codec so a generic document
// decodes into plain Go types. The BSON driver decodes an array held in an
// interface as its own named slice type (primitive.A) and may do the same
// for embedded documents; JSON documents and the query layer expect []any
// and map[string]any. Typed entities decode unchanged. NewCodec's wrapper
// reaches the codec only through Marshal and Unmarshal.
type bsonDocumentCodec[E any] struct {
	codec.Codec[E]
}

func (c bsonDocumentCodec[E]) Unmarshal(data []byte, v *E) error {
	if err := c.Codec.Unmarshal(data, v); err != nil {
		return err
	}
	normalizeDocument(v)
	return nil
}

// normalizeDocument rewrites the values of a decoded generic document in
// place; v of any other type is left alone.
func normalizeDocument[E any](v *E) {
	if m, ok := any(v).(*map[string]any); ok && *m != nil {
		for k, val := range *m {
			(*m)[k] = plainDocumentValue(val)
		}
	}
}

// plainDocumentValue returns val with every named string-keyed map
// converted to map[string]any and every named []any-shaped slice converted
// to []any, recursively.
func plainDocumentValue(val any) any {
	if val == nil {
		return nil
	}
	rv := reflect.ValueOf(val)
	switch rv.Kind() {
	case reflect.Map:
		if rv.Type().Key().Kind() != reflect.String || rv.Type().Elem().Kind() != reflect.Interface {
			return val
		}
		if rv.IsNil() {
			return val
		}
		out := make(map[string]any, rv.Len())
		iter := rv.MapRange()
		for iter.Next() {
			out[iter.Key().String()] = plainDocumentValue(iter.Value().Interface())
		}
		return out
	case reflect.Slice:
		if rv.Type().Elem().Kind() != reflect.Interface || rv.IsNil() {
			return val
		}
		out := make([]any, rv.Len())
		for i := range out {
			out[i] = plainDocumentValue(rv.Index(i).Interface())
		}
		return out
	default:
		return val
	}
}
