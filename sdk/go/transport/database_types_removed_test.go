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

package transport

import (
	"reflect"
	"strings"
	"testing"
)

// TestDatabaseInterface_HasNoUpdateDatabaseConnection asserts the database
// client offers no remote-connection verb: remote mode is removed (OD-9 A).
func TestDatabaseInterface_HasNoUpdateDatabaseConnection(t *testing.T) {
	client := reflect.TypeOf((*DatabaseClient)(nil)).Elem()
	if _, ok := client.MethodByName("UpdateDatabaseConnection"); ok {
		t.Fatal("DatabaseClient still declares UpdateDatabaseConnection")
	}
}

// TestDatabaseInfo_HasNoConnectionField asserts neither DatabaseInfo nor
// CreateDatabaseRequest carries a remote connection block.
func TestDatabaseInfo_HasNoConnectionField(t *testing.T) {
	for _, typ := range []reflect.Type{
		reflect.TypeOf(DatabaseInfo{}),
		reflect.TypeOf(CreateDatabaseRequest{}),
	} {
		if _, ok := typ.FieldByName("Connection"); ok {
			t.Errorf("%s still has a Connection field", typ.Name())
		}
		for i := 0; i < typ.NumField(); i++ {
			tag := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
			if tag == "connection" {
				t.Errorf("%s.%s carries the json tag \"connection\"", typ.Name(), typ.Field(i).Name)
			}
		}
	}
}
