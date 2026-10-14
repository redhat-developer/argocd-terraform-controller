/*
Copyright 2026 The Argo CD Terraform Controller Authors.

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

package runner

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"text/template"

	"github.com/hashicorp/terraform-exec/tfexec"
)

var (
	stateLockErrRegexp  = regexp.MustCompile(`(?s)Error acquiring the state lock`)
	stateLockInfoRegexp = regexp.MustCompile(`Lock Info:\n\s*ID:\s*([^\n]+)\n\s*Path:\s*([^\n]+)\n\s*Operation:\s*([^\n]+)\n\s*Who:\s*([^\n]+)\n\s*Version:\s*([^\n]+)\n\s*Created:\s*([^\n]+)(?:\n|$)`)
)

// StateLockError is returned when the state is locked by another process.
type StateLockError struct {
	originalError error

	ID        string
	Path      string
	Operation string
	Who       string
	Version   string
	Created   string
}

func (e *StateLockError) Error() string {
	if e.ID == "" {
		return fmt.Sprintf("error acquiring the state lock: %s", e.originalError.Error())
	}

	tmpl := `Lock Info:
  ID:        {{.ID}}
  Path:      {{.Path}}
  Operation: {{.Operation}}
  Who:       {{.Who}}
  Version:   {{.Version}}
  Created:   {{.Created}}
`
	t := template.Must(template.New("LockInfo").Parse(tmpl))
	var out strings.Builder
	if err := t.Execute(&out, e); err != nil {
		return "error acquiring the state lock"
	}
	return fmt.Sprintf("error acquiring the state lock: %v", out.String())
}

func (e *StateLockError) Unwrap() error { return e.originalError }

// execWrapper wraps tfexec.Terraform to normalise CLI errors so state locks
// can be detected.
type execWrapper struct {
	*tfexec.Terraform
}

func newExecWrapper(tf *tfexec.Terraform) *execWrapper {
	return &execWrapper{Terraform: tf}
}

func (t *execWrapper) Init(ctx context.Context, opts ...tfexec.InitOption) error {
	return normalizeError(t.Terraform.Init(ctx, opts...))
}

func (t *execWrapper) Apply(ctx context.Context, opts ...tfexec.ApplyOption) error {
	return normalizeError(t.Terraform.Apply(ctx, opts...))
}

func (t *execWrapper) Destroy(ctx context.Context, opts ...tfexec.DestroyOption) error {
	return normalizeError(t.Terraform.Destroy(ctx, opts...))
}

func (t *execWrapper) Plan(ctx context.Context, opts ...tfexec.PlanOption) (bool, error) {
	drifted, err := t.Terraform.Plan(ctx, opts...)
	return drifted, normalizeError(err)
}

func normalizeError(err error) error {
	if err == nil {
		return nil
	}
	trimmed := strings.TrimSpace(err.Error())
	switch {
	case stateLockInfoRegexp.MatchString(trimmed):
		m := stateLockInfoRegexp.FindStringSubmatch(trimmed)
		return &StateLockError{
			originalError: err,
			ID:            m[1],
			Path:          m[2],
			Operation:     m[3],
			Who:           m[4],
			Version:       m[5],
			Created:       m[6],
		}
	case stateLockErrRegexp.MatchString(trimmed):
		return &StateLockError{originalError: err}
	}
	return err
}
