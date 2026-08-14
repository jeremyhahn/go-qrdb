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

package main

import (
	"encoding/json"
	"reflect"

	"github.com/jeremyhahn/go-qrdb/pkg/entity"
)

// bridgeTestUser is a test entity registered for E2E integration testing
// of the entity-to-SQL bridge. Entities are a core feature of the database
// and are always present — no build tags required.
type bridgeTestUser struct {
	ID    uint64  `json:"id"`
	Name  string  `json:"name"`
	Email string  `json:"email"`
	Age   int64   `json:"age"`
	Score float64 `json:"score"`
}

func init() {
	eType := reflect.TypeOf(bridgeTestUser{})
	_ = entity.Global().Register(&entity.Registration{
		EntityType: "bridge_users",
		StructType: eType,
		Fields: []entity.FieldMeta{
			{GoName: "ID", JSONName: "id", GoType: reflect.TypeOf(uint64(0)), GoKind: reflect.Uint64, IsPrimaryKey: true},
			{GoName: "Name", JSONName: "name", GoType: reflect.TypeOf(""), GoKind: reflect.String, Indexed: true},
			{GoName: "Email", JSONName: "email", GoType: reflect.TypeOf(""), GoKind: reflect.String, Indexed: true, Unique: true},
			{GoName: "Age", JSONName: "age", GoType: reflect.TypeOf(int64(0)), GoKind: reflect.Int64},
			{GoName: "Score", JSONName: "score", GoType: reflect.TypeOf(float64(0)), GoKind: reflect.Float64},
		},
		IDFieldJSON: "id",
		IDFieldGo:   "ID",
		Database:    "",
		CodecType:   "json",
		NewFunc:     func() any { return &bridgeTestUser{} },
		MarshalFunc: json.Marshal,
		UnmarshalFunc: func(data []byte) (any, error) {
			u := &bridgeTestUser{}
			if err := json.Unmarshal(data, u); err != nil {
				return nil, err
			}
			return u, nil
		},
	})
}
