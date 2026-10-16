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

package sdk_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/abac"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
)

// This file is in the EXTERNAL test package (sdk_test), like
// types_ratelimit_reexport_test.go: an identifier missing from the
// re-export fails to compile here exactly as it would for a consumer.

// The aliases are the abac types themselves, not copies: each assignment
// compiles only if the two names denote one type.
var (
	_ *abac.ConditionParser                                 = (*sdk.ABACConditionParser)(nil)
	_ abac.CompiledCondition                                = sdk.ABACCompiledCondition(nil)
	_ *abac.ConditionParseError                             = (*sdk.ABACConditionParseError)(nil)
	_ func(map[string]interface{}) *abac.ConditionParser    = sdk.NewABACConditionParser
	_ func(map[string]interface{}) *sdk.ABACConditionParser = abac.NewConditionParser
)

// TestSDK_ABACConditionParserAliasesAreTheAbacTypes proves at run time that
// every re-exported name is the abac type, so a consumer validating a
// condition uses the server's parser and not a fork of it.
func TestSDK_ABACConditionParserAliasesAreTheAbacTypes(t *testing.T) {
	require.Equal(t, reflect.TypeOf(abac.ConditionParser{}), reflect.TypeOf(sdk.ABACConditionParser{}))
	require.Equal(t, reflect.TypeOf((*abac.CompiledCondition)(nil)).Elem(),
		reflect.TypeOf((*sdk.ABACCompiledCondition)(nil)).Elem())
	require.Equal(t, reflect.TypeOf(abac.ConditionParseError{}), reflect.TypeOf(sdk.ABACConditionParseError{}))
	require.Equal(t, reflect.ValueOf(abac.NewConditionParser).Pointer(),
		reflect.ValueOf(sdk.NewABACConditionParser).Pointer())
}

// TestSDK_ABACConditionParserParsesAndEvaluates parses a valid expression
// through the SDK names and evaluates the compiled condition both ways.
func TestSDK_ABACConditionParserParsesAndEvaluates(t *testing.T) {
	var parser *sdk.ABACConditionParser = sdk.NewABACConditionParser(nil)
	require.NotNil(t, parser)

	var cond sdk.ABACCompiledCondition
	cond, err := parser.Parse(`request.env == "prod"`)
	require.NoError(t, err)
	require.NotNil(t, cond)

	require.True(t, cond.Evaluate(&abac.EvalContext{Request: map[string]interface{}{"env": "prod"}}))
	require.False(t, cond.Evaluate(&abac.EvalContext{Request: map[string]interface{}{"env": "dev"}}))
}

// TestSDK_ABACConditionParserCompoundExpression parses a logical expression
// with policy variables supplied to the constructor.
func TestSDK_ABACConditionParserCompoundExpression(t *testing.T) {
	vars := map[string]interface{}{"env": "prod"}
	parser := sdk.NewABACConditionParser(vars)

	cond, err := parser.Parse(`request.env == ${env} and request.port > 1024`)
	require.NoError(t, err)

	ctx := &abac.EvalContext{
		Request:   map[string]interface{}{"env": "prod", "port": 8443},
		Variables: vars,
	}
	require.True(t, cond.Evaluate(ctx))

	ctx.Request["port"] = 80
	require.False(t, cond.Evaluate(ctx))
}

// TestSDK_ABACConditionParserRejectsEmptyExpression proves a parse failure
// surfaces as *sdk.ABACConditionParseError through errors.As.
func TestSDK_ABACConditionParserRejectsEmptyExpression(t *testing.T) {
	cond, err := sdk.NewABACConditionParser(nil).Parse("   ")
	require.Error(t, err)
	require.Nil(t, cond)

	var parseErr *sdk.ABACConditionParseError
	require.True(t, errors.As(err, &parseErr))
	require.NotEmpty(t, parseErr.Reason)
}

// TestSDK_ABACConditionParserRejectsZeroLengthExpression proves the typed
// error carries the expression it could not parse and a reason its Error
// text reports.
func TestSDK_ABACConditionParserRejectsZeroLengthExpression(t *testing.T) {
	_, err := sdk.NewABACConditionParser(map[string]interface{}{}).Parse("")
	require.Error(t, err)

	var parseErr *sdk.ABACConditionParseError
	require.True(t, errors.As(err, &parseErr))
	require.Empty(t, parseErr.Expression)
	require.NotEmpty(t, parseErr.Reason)
	require.Contains(t, parseErr.Error(), parseErr.Reason)
}
