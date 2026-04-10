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

//go:build codec_protobuf

package dao

import (
	"github.com/jeremyhahn/go-codec/pkg/protobuf"
	"google.golang.org/protobuf/proto"
)

// ProtoMessage is a constraint that requires types to implement proto.Message.
type ProtoMessage interface {
	proto.Message
}

// NewProtoBufCodec creates a new Protocol Buffers codec for types implementing proto.Message.
// This is a separate function because ProtoBuf has a type constraint requirement.
func NewProtoBufCodec[E ProtoMessage]() Codec[E] {
	return &codecWrapper[E]{
		codec:     protobuf.New[E](),
		codecType: CodecProtoBuf,
	}
}
