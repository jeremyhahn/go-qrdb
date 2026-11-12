// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build codec_msgpack && codec_yaml

package codec

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManager_Transcode(t *testing.T) {
	m := NewManager(&MsgPackCodec{}, &JSONCodec{})
	original := samplePayload()

	// Encode to storage format (msgpack).
	storageData, err := m.EncodeForStorage(original)
	require.NoError(t, err)

	// Transcode from storage to wire (msgpack -> json).
	var intermediate testPayload
	wireData, err := m.Transcode(storageData, &intermediate)
	require.NoError(t, err)
	require.NotEmpty(t, wireData)

	// Verify the wire output is valid JSON.
	var decoded testPayload
	err = m.DecodeFromWire(wireData, &decoded)
	require.NoError(t, err)
	assert.Equal(t, original, decoded)
}
