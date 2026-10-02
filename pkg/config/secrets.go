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
	"fmt"
	"os"
	"sort"
	"strings"
)

// Secret describes where the value of a build secret is read from.
// Exactly one of Src and Env is set.
type Secret struct {
	// Src is the path of a file holding the secret.
	Src string
	// Env is the name of an environment variable holding the secret.
	Env string
}

// Value reads the content of the secret.
func (s Secret) Value() ([]byte, error) {
	if s.Src != "" {
		return os.ReadFile(s.Src)
	}
	v, ok := os.LookupEnv(s.Env)
	if !ok {
		return nil, fmt.Errorf("environment variable %s is not set", s.Env)
	}
	return []byte(v), nil
}

// Secrets maps build secret ids to their sources. It is used as a flag value
// accepting the same syntax as `docker build --secret`:
//
//	id=mysecret,src=/path/to/file
//	id=mysecret,env=MY_VAR
//	id=MY_VAR                     (reads the environment variable MY_VAR)
type Secrets map[string]Secret

func (s *Secrets) String() string {
	var result []string
	for id, secret := range *s {
		if secret.Src != "" {
			result = append(result, fmt.Sprintf("id=%s,src=%s", id, secret.Src))
		} else {
			result = append(result, fmt.Sprintf("id=%s,env=%s", id, secret.Env))
		}
	}
	sort.Strings(result)
	return strings.Join(result, ";")
}

func (s *Secrets) Set(value string) error {
	var id, typ, src, env string
	for _, field := range strings.Split(value, ",") {
		kv := strings.SplitN(field, "=", 2)
		if len(kv) != 2 {
			return fmt.Errorf("invalid secret %q: expected key=value, got %q", value, field)
		}
		switch strings.ToLower(kv[0]) {
		case "id":
			id = kv[1]
		case "type":
			typ = kv[1]
		case "src", "source":
			src = kv[1]
		case "env":
			env = kv[1]
		default:
			return fmt.Errorf("invalid secret %q: unknown key %q", value, kv[0])
		}
	}
	if id == "" {
		return fmt.Errorf("invalid secret %q: id is required", value)
	}
	if src != "" && env != "" {
		return fmt.Errorf("invalid secret %q: src and env are mutually exclusive", value)
	}
	switch typ {
	case "":
	case "file":
		if env != "" {
			return fmt.Errorf("invalid secret %q: env can't be used with type=file", value)
		}
		if src == "" {
			return fmt.Errorf("invalid secret %q: src is required with type=file", value)
		}
	case "env":
		if src != "" {
			// docker accepts src as the variable name for type=env
			env, src = src, ""
		}
	default:
		return fmt.Errorf("invalid secret %q: unsupported type %q", value, typ)
	}
	if src == "" && env == "" {
		env = id
	}
	if *s == nil {
		*s = Secrets{}
	}
	(*s)[id] = Secret{Src: src, Env: env}
	return nil
}

func (s *Secrets) Type() string {
	return "secret"
}
