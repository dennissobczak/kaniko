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

package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	kConfig "github.com/GoogleContainerTools/kaniko/pkg/config"
	"github.com/GoogleContainerTools/kaniko/pkg/dockerfile"
	"github.com/GoogleContainerTools/kaniko/testutil"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/moby/buildkit/frontend/dockerfile/instructions"
)

func parseRun(t *testing.T, line string) *instructions.RunCommand {
	t.Helper()
	stages, _, err := dockerfile.Parse([]byte("FROM scratch\n" + line + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	return stages[0].Commands[0].(*instructions.RunCommand)
}

// owner returns mount options owning the secret by the current user, so tests
// don't need to run as root.
func owner() string {
	return fmt.Sprintf("uid=%d,gid=%d", os.Geteuid(), os.Getegid())
}

func withRootDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	original := kConfig.RootDir
	kConfig.RootDir = root
	t.Cleanup(func() { kConfig.RootDir = original })
	return root
}

func secretFile(t *testing.T, content string) kConfig.Secret {
	t.Helper()
	p := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(p, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return kConfig.Secret{Src: p}
}

func Test_mountSecrets_defaultTarget(t *testing.T) {
	root := withRootDir(t)
	past := time.Unix(1000, 0)
	if err := os.Chtimes(root, past, past); err != nil {
		t.Fatal(err)
	}
	secrets := kConfig.Secrets{"token": secretFile(t, "hunter2")}

	env, cleanup, err := mountSecrets(parseRun(t, "RUN --mount=type=secret,id=token,"+owner()+" true"), secrets, "/", nil)
	testutil.CheckNoError(t, err)
	testutil.CheckDeepEqual(t, 0, len(env))

	dst := filepath.Join(root, "run/secrets/token")
	content, err := os.ReadFile(dst)
	testutil.CheckNoError(t, err)
	testutil.CheckDeepEqual(t, "hunter2", string(content))
	fi, err := os.Stat(dst)
	testutil.CheckNoError(t, err)
	testutil.CheckDeepEqual(t, os.FileMode(0400), fi.Mode().Perm())

	testutil.CheckNoError(t, cleanup())
	if _, err := os.Lstat(filepath.Join(root, "run")); !os.IsNotExist(err) {
		t.Errorf("expected created directories to be removed, got %v", err)
	}
	fi, err = os.Stat(root)
	testutil.CheckNoError(t, err)
	if !fi.ModTime().Equal(past) {
		t.Errorf("expected mtime of %s to be restored to %v, got %v", root, past, fi.ModTime())
	}
}

func Test_mountSecrets_targetAndMode(t *testing.T) {
	root := withRootDir(t)
	secrets := kConfig.Secrets{"token": secretFile(t, "hunter2")}

	_, cleanup, err := mountSecrets(parseRun(t, "RUN --mount=type=secret,id=token,target=creds/tok,mode=0440,"+owner()+" true"), secrets, "/app", nil)
	testutil.CheckNoError(t, err)

	fi, err := os.Stat(filepath.Join(root, "app/creds/tok"))
	testutil.CheckNoError(t, err)
	testutil.CheckDeepEqual(t, os.FileMode(0440), fi.Mode().Perm())
	testutil.CheckNoError(t, cleanup())
}

func Test_mountSecrets_idFromTarget(t *testing.T) {
	root := withRootDir(t)
	secrets := kConfig.Secrets{".npmrc": secretFile(t, "//registry/:_authToken=x")}

	_, cleanup, err := mountSecrets(parseRun(t, "RUN --mount=type=secret,target=/root/.npmrc,"+owner()+" true"), secrets, "/", nil)
	testutil.CheckNoError(t, err)
	if _, err := os.Stat(filepath.Join(root, "root/.npmrc")); err != nil {
		t.Error(err)
	}
	testutil.CheckNoError(t, cleanup())
}

func Test_mountSecrets_env(t *testing.T) {
	root := withRootDir(t)
	t.Setenv("KANIKO_TEST_SECRET", "hunter2")
	secrets := kConfig.Secrets{"token": {Env: "KANIKO_TEST_SECRET"}}

	env, cleanup, err := mountSecrets(parseRun(t, "RUN --mount=type=secret,id=token,env=TOKEN true"), secrets, "/", nil)
	testutil.CheckNoError(t, err)
	testutil.CheckDeepEqual(t, []string{"TOKEN=hunter2"}, env)

	entries, err := os.ReadDir(root)
	testutil.CheckNoError(t, err)
	if len(entries) != 0 {
		t.Errorf("expected no file to be written for an env-only secret, got %v", entries)
	}
	testutil.CheckNoError(t, cleanup())
}

func Test_mountSecrets_missing(t *testing.T) {
	withRootDir(t)

	_, _, err := mountSecrets(parseRun(t, "RUN --mount=type=secret,id=token,required true"), nil, "/", nil)
	if err == nil || !strings.Contains(err.Error(), "required") {
		t.Errorf("expected a required secret error, got %v", err)
	}

	env, cleanup, err := mountSecrets(parseRun(t, "RUN --mount=type=secret,id=token true"), nil, "/", nil)
	testutil.CheckNoError(t, err)
	testutil.CheckDeepEqual(t, 0, len(env))
	testutil.CheckNoError(t, cleanup())
}

func Test_mountSecrets_existingTarget(t *testing.T) {
	root := withRootDir(t)
	existing := filepath.Join(root, "etc/passwd")
	if err := os.MkdirAll(filepath.Dir(existing), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(existing, []byte("root"), 0644); err != nil {
		t.Fatal(err)
	}
	secrets := kConfig.Secrets{
		"a": secretFile(t, "a"),
		"b": secretFile(t, "b"),
	}

	_, _, err := mountSecrets(parseRun(t, "RUN --mount=type=secret,id=a,"+owner()+" --mount=type=secret,id=b,target=/etc/passwd,"+owner()+" true"), secrets, "/", nil)
	if err == nil {
		t.Fatal("expected an error mounting over an existing file")
	}
	content, _ := os.ReadFile(existing)
	testutil.CheckDeepEqual(t, "root", string(content))
	if _, err := os.Lstat(filepath.Join(root, "run")); !os.IsNotExist(err) {
		t.Errorf("expected secrets mounted before the error to be removed, got %v", err)
	}
}

func Test_RunCommand_secrets(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "tok")
	t.Setenv("KANIKO_TEST_SECRET", "hunter2")
	secrets := kConfig.Secrets{
		"file": secretFile(t, "s3cret"),
		"env":  {Env: "KANIKO_TEST_SECRET"},
	}
	run := parseRun(t, fmt.Sprintf(`RUN --mount=type=secret,id=file,target=%s,%s --mount=type=secret,id=env,env=TOKEN test "$(cat %s)" = s3cret && test "$TOKEN" = hunter2`, target, owner(), target))

	for _, cmd := range []DockerCommand{
		&RunCommand{cmd: run, secrets: secrets},
		&RunMarkerCommand{cmd: run, secrets: secrets},
	} {
		cfg := &v1.Config{}
		testutil.CheckNoError(t, cmd.ExecuteCommand(cfg, dockerfile.NewBuildArgs(nil)))
		if _, err := os.Lstat(target); !os.IsNotExist(err) {
			t.Errorf("expected secret file to be removed after RUN, got %v", err)
		}
		for _, e := range cfg.Env {
			if strings.HasPrefix(e, "TOKEN=") {
				t.Errorf("secret leaked into the image config: %s", e)
			}
		}
	}
}

func Test_mountSecrets_expandsVariables(t *testing.T) {
	root := withRootDir(t)
	secrets := kConfig.Secrets{"token": secretFile(t, "hunter2")}

	_, cleanup, err := mountSecrets(parseRun(t, "RUN --mount=type=secret,id=${ID},target=${DIR}/tok,"+owner()+" true"), secrets, "/", []string{"ID=token", "DIR=/creds"})
	testutil.CheckNoError(t, err)
	if _, err := os.Stat(filepath.Join(root, "creds/tok")); err != nil {
		t.Error(err)
	}
	testutil.CheckNoError(t, cleanup())
}
