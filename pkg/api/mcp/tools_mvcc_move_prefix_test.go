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

package mcp

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/jeremyhahn/go-qrdb/pkg/api/errclass"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// moverMVCCService is an MVCC service that can move a prefix: it validates
// the request as the service does, records it, and answers rev or err.
type moverMVCCService struct {
	*mockMVCCService
	rev int64
	err error
	got *service.MovePrefixRequest
}

func (m *moverMVCCService) MovePrefix(_ context.Context, req *service.MovePrefixRequest) (int64, error) {
	if err := service.ValidateMovePrefix(req); err != nil {
		return 0, err
	}
	if m.err != nil {
		return 0, m.err
	}
	m.got = req
	return m.rev, nil
}

func movePrefixHandler(t *testing.T, svc service.MVCCKVServicer) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	t.Helper()
	for _, tool := range mvccTools(svc) {
		if tool.Tool.Name == "qrdb_mvcc_move_prefix" {
			return tool.Handler
		}
	}
	t.Fatal("tool qrdb_mvcc_move_prefix not found")
	return nil
}

func TestMovePrefix_ReturnsTheRevision(t *testing.T) {
	svc := &moverMVCCService{mockMVCCService: newMockMVCCService(), rev: 42}
	res, err := movePrefixHandler(t, svc)(context.Background(), makeReq(map[string]any{
		"src":           b64("db/stage/"),
		"dst":           b64("db/pages/"),
		"expected_keys": float64(2),
		"ops":           `[{"type":"put","key":"` + b64("db/meta") + `","value":"AP8="}]`,
		"compares":      `[{"key":"` + b64("db/meta") + `","target":"mod_revision","result":"equal","mod_revision":7}]`,
	}))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %+v", res.Content)
	}
	text := res.Content[0].(mcp.TextContent).Text
	if !strings.Contains(text, `"revision":42`) {
		t.Fatalf("result = %s, want revision 42", text)
	}
	if svc.got == nil || svc.got.Src != "db/stage/" || svc.got.Dst != "db/pages/" || svc.got.ExpectedKeys != 2 ||
		len(svc.got.Ops) != 1 || string(svc.got.Ops[0].Value) != "\x00\xff" ||
		len(svc.got.Compares) != 1 || svc.got.Compares[0].ModRevision != 7 {
		t.Fatalf("service got %+v", svc.got)
	}
}

func TestMovePrefix_CountMismatchIsAConflict(t *testing.T) {
	svc := &moverMVCCService{mockMVCCService: newMockMVCCService(), err: &service.CommandConflictError{Message: "live count 3, expected 2"}}
	_, err := movePrefixHandler(t, svc)(context.Background(), makeReq(map[string]any{
		"src": b64("a/"), "dst": b64("b/"), "expected_keys": float64(2),
	}))
	var conflict *service.CommandConflictError
	if !errors.As(err, &conflict) || errclass.Classify(err) != errclass.AlreadyExists {
		t.Fatalf("err = %v, want a CommandConflictError classed AlreadyExists", err)
	}
	if svc.got != nil {
		t.Fatal("a refused move recorded a write")
	}
}

func TestMovePrefix_InvalidRequestIsInvalidArgument(t *testing.T) {
	svc := &moverMVCCService{mockMVCCService: newMockMVCCService()}
	_, err := movePrefixHandler(t, svc)(context.Background(), makeReq(map[string]any{
		"src": b64("a/"), "dst": b64("a/b/"),
	}))
	if errclass.Classify(err) != errclass.InvalidArgument {
		t.Fatalf("err = %v (class %v), want InvalidArgument", err, errclass.Classify(err))
	}
}

func TestMovePrefix_InnerWithoutMoverIsUnimplemented(t *testing.T) {
	_, err := movePrefixHandler(t, newMockMVCCService())(context.Background(), makeReq(map[string]any{
		"src": b64("a/"), "dst": b64("b/"),
	}))
	var unsupported *service.PrefixMoveUnsupportedError
	if !errors.As(err, &unsupported) || errclass.Classify(err) != errclass.Unimplemented {
		t.Fatalf("err = %v, want *service.PrefixMoveUnsupportedError classed Unimplemented", err)
	}
}

func TestMovePrefix_MalformedArgumentsAreToolErrors(t *testing.T) {
	svc := &moverMVCCService{mockMVCCService: newMockMVCCService()}
	for name, args := range map[string]map[string]any{
		"src base64":     {"src": "!!", "dst": b64("b/")},
		"ops json":       {"src": b64("a/"), "dst": b64("b/"), "ops": "["},
		"op type":        {"src": b64("a/"), "dst": b64("b/"), "ops": `[{"type":"upsert"}]`},
		"compare target": {"src": b64("a/"), "dst": b64("b/"), "compares": `[{"target":"lease","result":"equal"}]`},
	} {
		res, err := movePrefixHandler(t, svc)(context.Background(), makeReq(args))
		if err != nil || res == nil || !res.IsError {
			t.Fatalf("%s: res=%+v err=%v, want a tool error result", name, res, err)
		}
	}
}
