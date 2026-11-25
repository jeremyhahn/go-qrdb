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

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// stubGroupClient records the GroupClient calls the binding makes and
// returns err (when set) from every one of them.
type stubGroupClient struct {
	err   error
	calls []string
	args  []any
}

func (s *stubGroupClient) record(name string, args ...any) {
	s.calls = append(s.calls, name)
	s.args = append(s.args, args...)
}

func (s *stubGroupClient) CreateGroup(_ context.Context, req *transport.CreateGroupRequest) (*transport.Group, error) {
	s.record("CreateGroup", req)
	if s.err != nil {
		return nil, s.err
	}
	return &transport.Group{ID: "g1", TenantID: req.TenantID, Name: req.Name}, nil
}

func (s *stubGroupClient) GetGroup(_ context.Context, id string) (*transport.Group, error) {
	s.record("GetGroup", id)
	if s.err != nil {
		return nil, s.err
	}
	return &transport.Group{ID: id}, nil
}

func (s *stubGroupClient) GetGroupByName(_ context.Context, req *transport.GetGroupByNameRequest) (*transport.Group, error) {
	s.record("GetGroupByName", req)
	if s.err != nil {
		return nil, s.err
	}
	return &transport.Group{ID: "g1", TenantID: req.TenantID, Name: req.Name}, nil
}

func (s *stubGroupClient) UpdateGroup(_ context.Context, req *transport.UpdateGroupRequest) (*transport.Group, error) {
	s.record("UpdateGroup", req)
	if s.err != nil {
		return nil, s.err
	}
	return &transport.Group{ID: req.ID, Name: req.Name}, nil
}

func (s *stubGroupClient) DeleteGroup(_ context.Context, id string) error {
	s.record("DeleteGroup", id)
	return s.err
}

func (s *stubGroupClient) ListGroups(_ context.Context, req *transport.ListGroupsRequest) (*transport.ListGroupsResponse, error) {
	s.record("ListGroups", req)
	if s.err != nil {
		return nil, s.err
	}
	return &transport.ListGroupsResponse{Groups: []*transport.Group{{ID: "g1"}}, Total: 1}, nil
}

func (s *stubGroupClient) AddGroupMember(_ context.Context, userID uint64, groupID string) error {
	s.record("AddGroupMember", userID, groupID)
	return s.err
}

func (s *stubGroupClient) RemoveGroupMember(_ context.Context, userID uint64, groupID string) error {
	s.record("RemoveGroupMember", userID, groupID)
	return s.err
}

func (s *stubGroupClient) ListGroupMembers(_ context.Context, groupID string) (*transport.ListGroupMembersResponse, error) {
	s.record("ListGroupMembers", groupID)
	if s.err != nil {
		return nil, s.err
	}
	return &transport.ListGroupMembersResponse{Members: []transport.GroupMember{{UserID: 7, Username: "alice", TenantID: "acme"}}}, nil
}

var _ transport.GroupClient = (*stubGroupClient)(nil)

func newStubGroupBinding(t *testing.T, stub *stubGroupClient) *GroupBinding {
	t.Helper()
	b := NewGroupBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	b.client = func() (transport.GroupClient, error) { return stub, nil }
	return b
}

func TestGroupBinding_CallsTheGroupClient(t *testing.T) {
	stub := &stubGroupClient{}
	b := newStubGroupBinding(t, stub)

	created, err := b.Create(&transport.CreateGroupRequest{TenantID: "acme", Name: "eng"})
	if err != nil || created.ID != "g1" || created.Name != "eng" {
		t.Fatalf("Create = %+v, %v", created, err)
	}
	if got, err := b.Get("g1"); err != nil || got.ID != "g1" {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	if got, err := b.GetByName(&transport.GetGroupByNameRequest{TenantID: "acme", Name: "eng"}); err != nil || got.Name != "eng" {
		t.Fatalf("GetByName = %+v, %v", got, err)
	}
	if got, err := b.Update(&transport.UpdateGroupRequest{ID: "g1", Name: "platform"}); err != nil || got.Name != "platform" {
		t.Fatalf("Update = %+v, %v", got, err)
	}
	if err := b.Delete("g1"); err != nil {
		t.Fatalf("Delete = %v", err)
	}
	if got, err := b.List(&transport.ListGroupsRequest{TenantID: "acme"}); err != nil || got.Total != 1 {
		t.Fatalf("List = %+v, %v", got, err)
	}
	if err := b.AddMember("7", "g1"); err != nil {
		t.Fatalf("AddMember = %v", err)
	}
	if err := b.RemoveMember("7", "g1"); err != nil {
		t.Fatalf("RemoveMember = %v", err)
	}
	members, err := b.Members("g1")
	if err != nil || len(members.Members) != 1 || members.Members[0].UserID != 7 {
		t.Fatalf("Members = %+v, %v", members, err)
	}

	want := []string{"CreateGroup", "GetGroup", "GetGroupByName", "UpdateGroup", "DeleteGroup", "ListGroups", "AddGroupMember", "RemoveGroupMember", "ListGroupMembers"}
	if !reflect.DeepEqual(stub.calls, want) {
		t.Fatalf("calls = %v, want %v", stub.calls, want)
	}
	// The user id arrives as a string and reaches the client as a uint64.
	for i, a := range stub.args {
		if u, ok := a.(uint64); ok && u != 7 {
			t.Fatalf("arg %d = %d, want user id 7", i, u)
		}
	}
}

func TestGroupBinding_BadUserIDRefused(t *testing.T) {
	stub := &stubGroupClient{}
	b := newStubGroupBinding(t, stub)
	for name, call := range map[string]func() error{
		"AddMember":    func() error { return b.AddMember("not-a-number", "g1") },
		"RemoveMember": func() error { return b.RemoveMember("-1", "g1") },
	} {
		err := call()
		var bad *InvalidUserIDError
		if !errors.As(err, &bad) {
			t.Fatalf("%s with a bad user id = %v, want *InvalidUserIDError", name, err)
		}
	}
	if len(stub.calls) != 0 {
		t.Fatalf("client called with a bad user id: %v", stub.calls)
	}
}

func TestGroupBinding_ClientErrorsReturned(t *testing.T) {
	boom := errors.New("boom")
	b := newStubGroupBinding(t, &stubGroupClient{err: boom})
	calls := map[string]func() error{
		"Create":       func() error { _, err := b.Create(&transport.CreateGroupRequest{Name: "eng"}); return err },
		"Get":          func() error { _, err := b.Get("g1"); return err },
		"GetByName":    func() error { _, err := b.GetByName(&transport.GetGroupByNameRequest{Name: "eng"}); return err },
		"Update":       func() error { _, err := b.Update(&transport.UpdateGroupRequest{ID: "g1"}); return err },
		"Delete":       func() error { return b.Delete("g1") },
		"List":         func() error { _, err := b.List(&transport.ListGroupsRequest{}); return err },
		"AddMember":    func() error { return b.AddMember("7", "g1") },
		"RemoveMember": func() error { return b.RemoveMember("7", "g1") },
		"Members":      func() error { _, err := b.Members("g1"); return err },
	}
	for name, call := range calls {
		if err := call(); !errors.Is(err, boom) {
			t.Errorf("%s = %v, want the client error", name, err)
		}
	}
}

func TestGroupBinding_NotConnected(t *testing.T) {
	b := NewGroupBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	if _, err := b.Get("g1"); err == nil {
		t.Fatal("Get without a connection returned no error")
	}
	if err := b.AddMember("7", "g1"); err == nil {
		t.Fatal("AddMember without a connection returned no error")
	}
}

// TestGroupBinding_SurfaceMatchesTypeScriptSDK pins the GroupBinding methods
// and arities sdk/typescript/src/wails.ts calls to the Go GroupBinding.
func TestGroupBinding_SurfaceMatchesTypeScriptSDK(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile(filepath.Join("..", "..", "sdk", "typescript", "src", "wails.ts"))
	if err != nil {
		t.Fatalf("read wails.ts: %v", err)
	}
	text := string(src)
	const anchor = "getBindings().main.GroupBinding)"
	start := strings.Index(text, anchor)
	if start < 0 {
		t.Fatalf("wails.ts no longer wraps main.GroupBinding (%q not found)", anchor)
	}
	block := text[start:]
	if end := strings.Index(block, "/** @inheritdoc */"); end > 0 {
		block = block[:end]
	}

	call := regexp.MustCompile(`\bb\.([A-Z]\w*)\(([^()]*(?:\([^()]*\)[^()]*)*)\)`)
	calls := map[string]int{}
	for _, m := range call.FindAllStringSubmatch(block, -1) {
		args := strings.TrimSpace(m[2])
		n := 0
		if args != "" {
			n = len(strings.Split(args, ","))
		}
		calls[m[1]] = n
	}

	typ := reflect.TypeOf(&GroupBinding{})
	want := []string{"AddMember", "Create", "Delete", "Get", "GetByName", "List", "Members", "RemoveMember", "Update"}
	names := make([]string, 0, len(calls))
	for name := range calls {
		names = append(names, name)
	}
	sort.Strings(names)
	if !reflect.DeepEqual(names, want) {
		t.Errorf("wails.ts calls GroupBinding.%v, want %v", names, want)
	}
	for _, name := range names {
		m, ok := typ.MethodByName(name)
		if !ok {
			t.Errorf("wails.ts calls GroupBinding.%s, which the Go GroupBinding does not export", name)
			continue
		}
		// NumIn counts the receiver.
		if got, want := m.Type.NumIn()-1, calls[name]; got != want {
			t.Errorf("GroupBinding.%s takes %d arguments; wails.ts passes %d", name, got, want)
		}
	}
}
