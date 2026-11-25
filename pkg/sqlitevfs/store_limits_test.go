// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package sqlitevfs

import (
	"encoding/base64"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic"
	"github.com/jeremyhahn/go-qrdb/pkg/api/rest"
)

// TestDefaultStageChunkFitsEveryTransportRequest asserts a default staging
// commit, its page bytes base64 encoded as the JSON codec sends them plus a
// key and framing allowance per page of the smallest SQLite page size, fits
// the default request limit of every transport.
func TestDefaultStageChunkFitsEveryTransportRequest(t *testing.T) {
	const minPageSize, perPageFraming = 512, 256
	request := base64.StdEncoding.EncodedLen(DefaultStageChunkBytes) + DefaultStageChunkBytes/minPageSize*perPageFraming
	for name, limit := range map[string]int{
		"grpc": grpc.DefaultMaxMessageBytes,
		"rest": rest.DefaultMaxBodyBytes,
		"quic": quic.DefaultMaxPayloadBytes,
	} {
		if request > limit {
			t.Errorf("default staging commit of %d bytes exceeds the %s request limit of %d", request, name, limit)
		}
	}
}
