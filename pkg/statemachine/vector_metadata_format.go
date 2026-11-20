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

package statemachine

import (
	"errors"
	"fmt"

	vectorStorage "github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
)

// commandMetadataFormat is the metadata format version this node writes on
// the vector commands it proposes.
const commandMetadataFormat = vectorStorage.MetadataFormatJSON

// upsertCreatedData is the result data of an upsert apply that created the
// vector; an upsert that replaced one returns no data.
var upsertCreatedData = []byte{1}

// upsertResultData returns the result data of an upsert apply.
func upsertResultData(created bool) []byte {
	if created {
		return upsertCreatedData
	}
	return nil
}

// UpsertCreated reports whether the data of an upsert apply result says the
// vector was created (it did not exist when the command applied).
func UpsertCreated(data []byte) bool {
	return len(data) == 1 && data[0] == upsertCreatedData[0]
}

// vectorNotFound reports whether a vector read failed because no vector
// with the ID is stored.
func vectorNotFound(err error) bool {
	return errors.Is(err, vectorStorage.ErrVectorNotFound)
}

// commandMetadata returns the stored form of a command's metadata in the
// command's format: canonical JSON for MetadataFormatJSON, and for a command
// without a format version the verbatim %v strings it always stored.
func commandMetadata(format uint8, m map[string]interface{}) map[string]string {
	out := make(map[string]string, len(m))
	for k, val := range m {
		if format == vectorStorage.MetadataFormatJSON {
			out[k] = vectorStorage.EncodeMetadataValue(val)
			continue
		}
		out[k] = fmt.Sprintf("%v", val)
	}
	return out
}

// mergeCommandMetadata returns the stored metadata and format of a record
// after a metadata write in the command's format: with merge the existing
// values are kept (upgraded to JSON when a JSON write lands in a legacy
// record, or a legacy write in a JSON record, so a record never mixes
// formats), otherwise they are replaced.
func mergeCommandMetadata(existing map[string]string, existingFormat, cmdFormat uint8,
	m map[string]interface{}, merge bool) (map[string]string, uint8) {
	format := cmdFormat
	if merge && existingFormat == vectorStorage.MetadataFormatJSON {
		format = vectorStorage.MetadataFormatJSON
	}
	out := make(map[string]string, len(existing)+len(m))
	if merge {
		stored := existing
		if format != existingFormat {
			stored = vectorStorage.UpgradeMetadata(existingFormat, existing)
		}
		for k, val := range stored {
			out[k] = val
		}
	}
	for k, val := range commandMetadata(format, m) {
		out[k] = val
	}
	return out, format
}
