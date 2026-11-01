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

package objstore

import (
	"errors"
	"strings"
	"testing"

	objcommon "github.com/jeremyhahn/go-objstore/pkg/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// inlineCredentialValue is a recognisable value no refusal may echo.
const inlineCredentialValue = "inline-credential-value-must-not-leak"

func TestRejectCredentialSettings_RefusesACredentialSetting(t *testing.T) {
	for _, setting := range []string{"secret_key", "access_key"} {
		t.Run(setting, func(t *testing.T) {
			err := RejectCredentialSettings(map[string]string{
				"region": "us-east-1",
				setting:  inlineCredentialValue,
			})
			var settingErr *CredentialSettingError
			require.ErrorAs(t, err, &settingErr)
			assert.Equal(t, setting, settingErr.Setting)
			assert.Contains(t, err.Error(), `"`+setting+`"`)
			assert.NotContains(t, err.Error(), inlineCredentialValue)

			var cause *objcommon.CredentialSettingError
			require.True(t, errors.As(err, &cause), "the go-objstore cause must stay reachable")
			assert.Equal(t, setting, cause.Setting)
		})
	}
}

func TestRejectCredentialSettings_AcceptsSettingsWithoutACredential(t *testing.T) {
	cases := map[string]map[string]string{
		"nil":   nil,
		"empty": {},
		"plain": {"region": "us-east-1", "endpoint": "https://db.example.com"},
	}
	for name, settings := range cases {
		t.Run(name, func(t *testing.T) {
			assert.NoError(t, RejectCredentialSettings(settings))
		})
	}
}

func TestCredentialSettingError_NamesTheSettingNeverTheValue(t *testing.T) {
	cause := errors.New("cause")
	err := &CredentialSettingError{Setting: "secret_key", Err: cause}
	assert.Equal(t, `objstore: setting "secret_key" carries a credential inline; reference it by file`, err.Error())
	assert.Same(t, cause, errors.Unwrap(err))
	assert.False(t, strings.Contains(err.Error(), inlineCredentialValue))
}
