/*
Copyright 2026 Google LLC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/GoogleContainerTools/kaniko/testutil"
)

func TestSecrets_Set(t *testing.T) {
	tests := []struct {
		value    string
		expected Secret
		wantErr  bool
	}{
		{value: "id=a,src=/path", expected: Secret{Src: "/path"}},
		{value: "id=a,source=/path", expected: Secret{Src: "/path"}},
		{value: "type=file,id=a,src=/path", expected: Secret{Src: "/path"}},
		{value: "id=a,env=VAR", expected: Secret{Env: "VAR"}},
		{value: "type=env,id=a,src=VAR", expected: Secret{Env: "VAR"}},
		{value: "id=a", expected: Secret{Env: "a"}},
		{value: "src=/path", wantErr: true},
		{value: "id=a,src=/path,env=VAR", wantErr: true},
		{value: "type=file,id=a", wantErr: true},
		{value: "type=ssh,id=a", wantErr: true},
		{value: "id=a,unknown=b", wantErr: true},
		{value: "id", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.value, func(t *testing.T) {
			var s Secrets
			err := s.Set(test.value)
			testutil.CheckError(t, test.wantErr, err)
			if !test.wantErr {
				testutil.CheckDeepEqual(t, test.expected, s["a"])
			}
		})
	}
}

func TestSecret_Value(t *testing.T) {
	p := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(p, []byte("from-file"), 0600); err != nil {
		t.Fatal(err)
	}
	v, err := Secret{Src: p}.Value()
	testutil.CheckErrorAndDeepEqual(t, false, err, "from-file", string(v))

	t.Setenv("KANIKO_TEST_SECRET", "from-env")
	v, err = Secret{Env: "KANIKO_TEST_SECRET"}.Value()
	testutil.CheckErrorAndDeepEqual(t, false, err, "from-env", string(v))

	_, err = Secret{Env: "KANIKO_TEST_SECRET_UNSET"}.Value()
	testutil.CheckError(t, true, err)
}
