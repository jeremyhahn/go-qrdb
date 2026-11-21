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

package service

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/dao"
)

// collectionCodecGoldens are documents {"id":"7","name":"a","n":1.5} as
// the removed pkg/codec wrapper wrote them to disk. Collections written
// before the switch to go-codec must still decode.
var collectionCodecGoldens = map[dao.CodecType]string{
	dao.CodecMsgPack: "83a26964a137a46e616d65a161a16ecb3ff8000000000000",
	dao.CodecBSON:    "2600000002696400020000003700026e616d6500020000006100016e00000000000000f83f00",
	dao.CodecYAML:    "69643a202237220a226e223a20312e350a6e616d653a20610a",
}

func TestCollectionCodecCompat_GoldenDocumentsDecode(t *testing.T) {
	for ct, golden := range collectionCodecGoldens {
		t.Run(string(ct), func(t *testing.T) {
			data, err := hex.DecodeString(golden)
			require.NoError(t, err)
			c, err := dao.NewCodec[map[string]any](ct)
			require.NoError(t, err)

			var entity dao.DynamicEntity
			require.NoError(t, unmarshalDocument(data, &entity, c))
			assert.Equal(t, uint64(7), entity.ID)
			assert.Equal(t, "a", entity.Fields["name"])
			assert.InDelta(t, 1.5, entity.Fields["n"], 0)
			assert.NotContains(t, entity.Fields, "id")
		})
	}
}

func TestCollectionCodecCompat_RoundTripEveryDocumentCodec(t *testing.T) {
	for _, ct := range dao.DocumentCodecs() {
		t.Run(string(ct), func(t *testing.T) {
			c, err := dao.NewCodec[map[string]any](ct)
			require.NoError(t, err)

			in := &dao.DynamicEntity{ID: 7, Fields: map[string]any{"name": "a", "n": 1.5}}
			data, err := marshalDocument(in, c)
			require.NoError(t, err)

			var out dao.DynamicEntity
			require.NoError(t, unmarshalDocument(data, &out, c))
			assert.Equal(t, uint64(7), out.ID)
			assert.Equal(t, "a", out.Fields["name"])
			assert.InDelta(t, 1.5, out.Fields["n"], 0)
		})
	}
}

// TestCollectionCodecCompat_LosslessDocumentRoundTrip proves every storage
// codec returns a JSON document unchanged: a null field stays present and
// null, nested objects come back as string-keyed maps, and the quota charge
// on write equals the release on delete.
func TestCollectionCodecCompat_LosslessDocumentRoundTrip(t *testing.T) {
	for _, ct := range dao.DocumentCodecs() {
		t.Run(string(ct), func(t *testing.T) {
			c, err := dao.NewCodec[map[string]any](ct)
			require.NoError(t, err)

			in := &dao.DynamicEntity{ID: 9, Fields: map[string]any{
				"name":     "a",
				"optional": nil,
				"profile":  map[string]any{"name": "a"},
				"items":    []any{map[string]any{"k": "v"}, 1.5},
			}}
			wantJSON, err := json.Marshal(in.Fields)
			require.NoError(t, err)

			data, err := marshalDocument(in, c)
			require.NoError(t, err)
			var out dao.DynamicEntity
			require.NoError(t, unmarshalDocument(data, &out, c))

			gotJSON, err := json.Marshal(out.Fields)
			require.NoError(t, err)
			assert.JSONEq(t, string(wantJSON), string(gotJSON))

			v, ok := out.Fields["optional"]
			assert.True(t, ok, "null field dropped")
			assert.Nil(t, v)

			inBytes := DocumentBodyBytes(in.Fields)
			assert.Positive(t, inBytes)
			assert.Equal(t, inBytes, DocumentBodyBytes(out.Fields))
		})
	}
}

func TestCollectionCodecCompat_ProtobufRefused(t *testing.T) {
	svc := newTestCollectionService()
	_, err := svc.CreateCollection(context.Background(), &CreateCollectionRequest{
		Name:         "users",
		Database:     "mydb",
		StorageCodec: string(dao.CodecProtoBuf),
	})
	assert.IsType(t, &CollectionInvalidCodecError{}, err)
}

// TestCollectionCodecCompat_TOMLRefused proves a collection cannot store
// documents as TOML: TOML has no null, so null fields would be lost.
func TestCollectionCodecCompat_TOMLRefused(t *testing.T) {
	svc := newTestCollectionService()
	_, err := svc.CreateCollection(context.Background(), &CreateCollectionRequest{
		Name:         "users",
		Database:     "mydb",
		StorageCodec: string(dao.CodecTOML),
	})
	assert.IsType(t, &CollectionInvalidCodecError{}, err)
}

func TestCollectionCodecCompat_EveryDocumentCodecAccepted(t *testing.T) {
	svc := newTestCollectionService()
	for _, ct := range dao.DocumentCodecs() {
		info, err := svc.CreateCollection(context.Background(), &CreateCollectionRequest{
			Name:         "c_" + string(ct),
			Database:     "mydb",
			StorageCodec: string(ct),
		})
		require.NoError(t, err, "codec %s", ct)
		assert.Equal(t, ct, svc.resolveCodec(info).Type())
	}
}
