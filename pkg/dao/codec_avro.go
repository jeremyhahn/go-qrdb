// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-dragondb.
//
// go-dragondb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

//go:build codec_avro

package dao

import (
	"github.com/jeremyhahn/go-codec"
	"github.com/jeremyhahn/go-codec/pkg/avro"
)

func newAvroCodec[E any]() codec.Codec[E] {
	return avro.New[E]()
}
