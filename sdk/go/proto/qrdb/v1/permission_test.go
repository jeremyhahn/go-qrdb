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

package qrdbv1_test

import (
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"

	qrdbv1 "github.com/jeremyhahn/go-qrdb/sdk/go/proto/qrdb/v1"
)

// TestPermissionOptionIsPublished pins what a .proto outside go-qrdb depends
// on: the import path, the option's full name and its field number. Changing
// any of them breaks every service that imports the file.
func TestPermissionOptionIsPublished(t *testing.T) {
	fd, err := protoregistry.GlobalFiles.FindFileByPath("qrdb/v1/permission.proto")
	if err != nil {
		t.Fatalf("qrdb/v1/permission.proto is not registered: %v", err)
	}
	if fd != qrdbv1.File_qrdb_v1_permission_proto {
		t.Fatal("the registered file is not this package's")
	}
	xd := qrdbv1.E_Permission.TypeDescriptor()
	if xd.FullName() != "qrdb.v1.permission" || xd.Number() != 51000 {
		t.Fatalf("option = %s #%d, want qrdb.v1.permission #51000", xd.FullName(), xd.Number())
	}
	if got := xd.ContainingMessage().FullName(); got != "google.protobuf.MethodOptions" {
		t.Fatalf("option extends %s, want google.protobuf.MethodOptions", got)
	}
}

func TestPermissionOptionRoundTripsOnMethodOptions(t *testing.T) {
	opts := &descriptorpb.MethodOptions{}
	if proto.HasExtension(opts, qrdbv1.E_Permission) {
		t.Fatal("empty MethodOptions reports the option")
	}
	proto.SetExtension(opts, qrdbv1.E_Permission, "kv:put")
	raw, err := proto.Marshal(opts)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	back := &descriptorpb.MethodOptions{}
	if err := proto.Unmarshal(raw, back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got, _ := proto.GetExtension(back, qrdbv1.E_Permission).(string); got != "kv:put" {
		t.Fatalf("permission = %q, want kv:put", got)
	}
}
