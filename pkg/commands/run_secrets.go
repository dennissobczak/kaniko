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
	"path"
	"path/filepath"

	kConfig "github.com/GoogleContainerTools/kaniko/pkg/config"
	"github.com/GoogleContainerTools/kaniko/pkg/util"
	"github.com/moby/buildkit/frontend/dockerfile/instructions"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

const (
	defaultSecretDir  = "/run/secrets"
	defaultSecretMode = 0400
)

// mountSecrets makes the secrets requested with RUN --mount=type=secret
// available to the command. It returns the environment variables to add to the
// command, and a cleanup function which removes every file it wrote. Kaniko
// can't bind mount, so secret files are written to the filesystem: cleanup must
// run before the filesystem is snapshotted or the secrets end up in the layer.
func mountSecrets(cmdRun *instructions.RunCommand, secrets kConfig.Secrets, workdir string, replacementEnvs []string) (env []string, cleanup func() error, err error) {
	var undos []func() error
	undoAll := func() error {
		var firstErr error
		for i := len(undos) - 1; i >= 0; i-- {
			if err := undos[i](); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		return firstErr
	}
	defer func() {
		if err != nil {
			if cerr := undoAll(); cerr != nil {
				logrus.Errorf("Removing secrets: %v", cerr)
			}
		}
	}()

	// The parser defers evaluating --mount until variables can be expanded.
	err = cmdRun.Expand(func(word string) (string, error) {
		return util.ResolveEnvironmentReplacement(word, replacementEnvs, false)
	})
	if err != nil {
		return nil, nil, errors.Wrap(err, "parsing RUN --mount")
	}

	for _, m := range instructions.GetMounts(cmdRun) {
		if m.Type != instructions.MountTypeSecret {
			logrus.Warnf("RUN --mount=type=%s is not supported, ignoring it", m.Type)
			continue
		}

		id := m.CacheID
		if m.Source != "" {
			id = m.Source
		}
		if id == "" {
			id = path.Base(m.Target)
		}

		secret, ok := secrets[id]
		if !ok {
			if m.Required {
				return nil, nil, fmt.Errorf("secret %s is required but was not provided, use --secret id=%s,src=<path>", id, id)
			}
			logrus.Infof("Secret %s was not provided, skipping its mount", id)
			continue
		}
		value, err := secret.Value()
		if err != nil {
			return nil, nil, errors.Wrapf(err, "reading secret %s", id)
		}

		if m.Env != nil {
			name := *m.Env
			if name == "" {
				name = id
			}
			env = append(env, name+"="+string(value))
		}

		target := m.Target
		if target == "" && m.Env == nil {
			target = path.Join(defaultSecretDir, id)
		}
		if target == "" {
			continue
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join("/", workdir, target)
		}

		mode := os.FileMode(defaultSecretMode)
		if m.Mode != nil {
			mode = os.FileMode(*m.Mode)
		}
		uid, gid := 0, 0
		if m.UID != nil {
			uid = int(*m.UID)
		}
		if m.GID != nil {
			gid = int(*m.GID)
		}

		undo, err := writeSecretFile(filepath.Join(kConfig.RootDir, target), value, mode, uid, gid)
		if err != nil {
			return nil, nil, errors.Wrapf(err, "mounting secret %s at %s", id, target)
		}
		undos = append(undos, undo)
	}
	return env, undoAll, nil
}

// writeSecretFile writes value to dst, creating missing parent directories.
// The returned function removes the file and the directories it created, and
// resets the modification time of the closest existing parent so the
// snapshot doesn't see a change.
func writeSecretFile(dst string, value []byte, mode os.FileMode, uid, gid int) (func() error, error) {
	if _, err := os.Lstat(dst); err == nil {
		return nil, fmt.Errorf("%s already exists, secrets can't be mounted over existing files", dst)
	}

	var created []string
	parent := filepath.Dir(dst)
	for {
		if _, err := os.Lstat(parent); err == nil {
			break
		}
		created = append(created, parent)
		parent = filepath.Dir(parent)
	}
	parentInfo, err := os.Stat(parent)
	if err != nil {
		return nil, err
	}

	undo := func() error {
		if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
			return err
		}
		// created is ordered deepest first
		for _, dir := range created {
			if err := os.Remove(dir); err != nil && !os.IsNotExist(err) {
				// The command added its own files there; keep them.
				logrus.Debugf("Not removing %s: %v", dir, err)
				return nil
			}
		}
		return os.Chtimes(parent, parentInfo.ModTime(), parentInfo.ModTime())
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return nil, err
	}
	if err := writeFileWithOwner(dst, value, mode, uid, gid); err != nil {
		if uerr := undo(); uerr != nil {
			logrus.Errorf("Removing secret file %s: %v", dst, uerr)
		}
		return nil, err
	}
	return undo, nil
}

func writeFileWithOwner(dst string, value []byte, mode os.FileMode, uid, gid int) error {
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err := f.Write(value); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if uid != os.Geteuid() || gid != os.Getegid() {
		if err := os.Chown(dst, uid, gid); err != nil {
			return err
		}
	}
	// Chmod after Chown, which may clear setuid/setgid bits
	return os.Chmod(dst, mode)
}
