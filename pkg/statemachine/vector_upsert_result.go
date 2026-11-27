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

	vectorStorage "github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
)

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
