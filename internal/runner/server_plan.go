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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	securejoin "github.com/cyphar/filepath-securejoin"
	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"
	"google.golang.org/protobuf/protoadapt"
)

func (r *Server) showPlanFile(ctx context.Context, planPath string) (*tfjson.Plan, error) {
	log := r.log(ctx)
	r.tf.SetStdout(nil)
	r.tf.SetStderr(nil)
	defer r.initLogger(log)
	return r.tf.ShowPlanFile(ctx, planPath)
}

func (r *Server) showPlanFileRaw(ctx context.Context, planPath string) (string, error) {
	log := r.log(ctx)
	r.tf.SetStdout(nil)
	r.tf.SetStderr(nil)
	defer r.initLogger(log)
	return r.tf.ShowPlanFileRaw(ctx, planPath)
}

// sanitizeLog masks variable values that terraform echoes back when a
// generated.auto.tfvars.json entry is invalid.
func sanitizeLog(log string) string {
	lines := strings.Split(log, "\n")
	for i := range lines {
		if !strings.Contains(lines[i], "on generated.auto.tfvars.json line") || i+1 >= len(lines) {
			continue
		}
		prefix, payload, ok := strings.Cut(lines[i+1], ": ")
		if !ok {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(payload), &obj); err != nil {
			continue
		}
		for k := range obj {
			obj[k] = "***"
		}
		if masked, err := json.Marshal(obj); err == nil {
			lines[i+1] = prefix + ": " + string(masked)
		}
	}
	return strings.Join(lines, "\n")
}

func (r *Server) quietPlan(ctx context.Context, opts ...tfexec.PlanOption) (bool, error) {
	log := r.log(ctx)
	r.tf.SetStdout(nil)
	errBuf := &bytes.Buffer{}
	r.tf.SetStderr(errBuf)
	defer r.initLogger(log)

	diff, err := r.tf.Plan(ctx, opts...)
	var lockErr *StateLockError
	if err != nil && !errors.As(err, &lockErr) {
		fmt.Fprint(os.Stderr, sanitizeLog(errBuf.String()))
		err = errors.New(sanitizeLog(err.Error()))
	}
	return diff, err
}

// Plan runs terraform plan.
func (r *Server) Plan(ctx context.Context, req *PlanRequest) (*PlanReply, error) {
	if err := r.validateInstanceID(req.TfInstance); err != nil {
		return nil, err
	}
	ctx, cancel := r.cancelOnDone(ctx)
	defer cancel()
	log := r.log(ctx)

	var opts []tfexec.PlanOption
	if req.Out != "" {
		opts = append(opts, tfexec.Out(req.Out))
	} else {
		log.Info("backend is disabled; no plan file will be produced")
	}
	if !req.Refresh {
		opts = append(opts, tfexec.Refresh(false))
	}
	if req.Destroy {
		opts = append(opts, tfexec.Destroy(true))
	}
	if req.LockTimeout != "" {
		opts = append(opts, tfexec.LockTimeout(req.LockTimeout))
	}
	for _, t := range req.Targets {
		opts = append(opts, tfexec.Target(t))
	}
	for _, path := range r.terraform.Spec.TfVarsFiles {
		file, err := securejoin.SecureJoin(req.SourceRefRootDir, path)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(file)
		if err != nil {
			return nil, fmt.Errorf("tfvars file %q not found: %w", path, err)
		}
		if info.IsDir() {
			return nil, fmt.Errorf("tfvars path %q is a directory", path)
		}
		opts = append(opts, tfexec.VarFile(file))
	}

	drifted, err := r.quietPlan(ctx, opts...)
	if err != nil {
		log.Error(err, "plan failed")
		return nil, lockErrStatus(err, func(id string) protoadapt.MessageV1 {
			return &PlanReply{Message: replyNotOK, StateLockIdentifier: id}
		})
	}

	planCreated := false
	if req.Out != "" {
		p, err := r.showPlanFile(ctx, req.Out)
		if err != nil {
			return nil, fmt.Errorf("unable to read plan file: %w", err)
		}
		planCreated = !isEmptyPlan(p)
	}
	return &PlanReply{Message: "ok", Drifted: drifted, PlanCreated: planCreated}, nil
}

func isEmptyPlan(p *tfjson.Plan) bool {
	return p.PlannedValues != nil &&
		p.PlannedValues.Outputs == nil &&
		(p.PlannedValues.RootModule == nil || p.PlannedValues.RootModule.Resources == nil) &&
		p.ResourceChanges == nil &&
		p.PriorState == nil &&
		p.OutputChanges == nil
}

// ShowPlanFileRaw returns the human readable plan.
func (r *Server) ShowPlanFileRaw(ctx context.Context, req *ShowPlanFileRawRequest) (*ShowPlanFileRawReply, error) {
	if err := r.validateInstanceID(req.TfInstance); err != nil {
		return nil, err
	}
	raw, err := r.showPlanFileRaw(ctx, req.Filename)
	if err != nil {
		return nil, err
	}
	return &ShowPlanFileRawReply{RawOutput: raw}, nil
}

// ShowPlanFile returns the plan as JSON.
func (r *Server) ShowPlanFile(ctx context.Context, req *ShowPlanFileRequest) (*ShowPlanFileReply, error) {
	if err := r.validateInstanceID(req.TfInstance); err != nil {
		return nil, err
	}
	p, err := r.showPlanFile(ctx, req.Filename)
	if err != nil {
		return nil, err
	}
	out, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	return &ShowPlanFileReply{JsonOutput: out}, nil
}
