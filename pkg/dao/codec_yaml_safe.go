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
	"fmt"

	"github.com/jeremyhahn/go-codec"
)

// yamlPanicSafeCodec wraps the go-codec YAML codec so a value yaml.v3
// cannot encode (a func or channel, for example) returns an encoding error.
// yaml.v3 panics on such values instead of returning one, and a panic in a
// request path must not take the process down. NewCodec's wrapper reaches
// the codec only through Marshal and Unmarshal.
type yamlPanicSafeCodec[E any] struct {
	codec.Codec[E]
}

func (c yamlPanicSafeCodec[E]) Marshal(v E) (data []byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			data = nil
			err = SerializationFailedError{
				EntityType: "yaml",
				Cause:      &CodecPanicError{Codec: CodecYAML, Value: fmt.Sprint(r)},
			}
		}
	}()
	return c.Codec.Marshal(v)
}
