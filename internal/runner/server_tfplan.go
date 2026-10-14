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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/redhat-developer/argocd-terraform-controller/api/planid"
	infrav1 "github.com/redhat-developer/argocd-terraform-controller/api/v1alpha1"
	"github.com/redhat-developer/argocd-terraform-controller/internal/plan"
)

const (
	readableSuffix = "-readable"
	jsonSuffix     = ".json"
)

// SaveTFPlan stores the binary plan in Secret(s) and, depending on
// spec.storeReadablePlan, a readable copy in a ConfigMap (human) or Secret
// (json).
func (r *Server) SaveTFPlan(ctx context.Context, req *SaveTFPlanRequest) (*SaveTFPlanReply, error) {
	if err := r.validateInstanceID(req.TfInstance); err != nil {
		return nil, err
	}
	log := r.log(ctx)
	tf := r.terraform

	var tfplan []byte
	if req.BackendCompletelyDisable {
		tfplan = []byte("dummy plan")
	} else {
		var err error
		if tfplan, err = os.ReadFile(filepath.Join(r.tf.WorkingDir(), TFPlanName)); err != nil {
			return nil, fmt.Errorf("error reading plan file: %w", err)
		}
	}

	planID := planid.GetPlanID(req.Revision)
	binary := plan.New(req.Name, req.Namespace, tf.WorkspaceName(), req.Uuid, planID, tfplan)
	if err := r.replacePlanSecrets(ctx, binary, ""); err != nil {
		return nil, err
	}

	reply := &SaveTFPlanReply{Message: "ok"}
	switch tf.Spec.StoreReadablePlan {
	case infrav1.StoreReadablePlanJSON:
		p, err := r.showPlanFile(ctx, TFPlanName)
		if err != nil {
			return nil, fmt.Errorf("unable to render json plan: %w", err)
		}
		data, err := json.Marshal(p)
		if err != nil {
			return nil, err
		}
		if err := r.replacePlanSecrets(ctx, plan.New(req.Name, req.Namespace, tf.WorkspaceName(), req.Uuid, planID, data), jsonSuffix); err != nil {
			return nil, err
		}
		reply.ReadablePlanName = plan.ObjectName(tf.WorkspaceName(), req.Name, jsonSuffix)
	case infrav1.StoreReadablePlanHuman:
		raw, err := r.showPlanFileRaw(ctx, TFPlanName)
		if err != nil {
			return nil, fmt.Errorf("unable to render human readable plan: %w", err)
		}
		if err := r.replacePlanConfigMaps(ctx, plan.New(req.Name, req.Namespace, tf.WorkspaceName(), req.Uuid, planID, []byte(raw)), readableSuffix); err != nil {
			return nil, err
		}
		reply.ReadablePlanName = plan.ObjectName(tf.WorkspaceName(), req.Name, readableSuffix)
	}

	log.Info("saved plan", "planID", planID, "readablePlan", reply.ReadablePlanName)
	return reply, nil
}

func (r *Server) replacePlanSecrets(ctx context.Context, p *plan.Plan, suffix string) error {
	existing := &corev1.SecretList{}
	labels := plan.Labels(r.terraform.Name, r.terraform.WorkspaceName(), suffix)
	if err := r.List(ctx, existing, client.InNamespace(r.terraform.Namespace), client.MatchingLabels(labels)); err != nil {
		return fmt.Errorf("unable to list plan secrets: %w", err)
	}
	for i := range existing.Items {
		if err := r.Delete(ctx, &existing.Items[i]); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("unable to delete plan secret %s: %w", existing.Items[i].Name, err)
		}
	}
	secrets, err := p.ToSecrets(suffix)
	if err != nil {
		return err
	}
	for _, s := range secrets {
		if err := r.Create(ctx, s); err != nil {
			return fmt.Errorf("unable to create plan secret %s: %w", s.Name, err)
		}
	}
	return nil
}

func (r *Server) replacePlanConfigMaps(ctx context.Context, p *plan.Plan, suffix string) error {
	existing := &corev1.ConfigMapList{}
	labels := plan.Labels(r.terraform.Name, r.terraform.WorkspaceName(), suffix)
	if err := r.List(ctx, existing, client.InNamespace(r.terraform.Namespace), client.MatchingLabels(labels)); err != nil {
		return fmt.Errorf("unable to list plan configmaps: %w", err)
	}
	for i := range existing.Items {
		if err := r.Delete(ctx, &existing.Items[i]); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("unable to delete plan configmap %s: %w", existing.Items[i].Name, err)
		}
	}
	for _, cm := range p.ToConfigMaps(suffix) {
		if err := r.Create(ctx, cm); err != nil {
			return fmt.Errorf("unable to create plan configmap %s: %w", cm.Name, err)
		}
	}
	return nil
}

// LoadTFPlan writes the pending plan from its Secret(s) into the working dir.
func (r *Server) LoadTFPlan(ctx context.Context, req *LoadTFPlanRequest) (*LoadTFPlanReply, error) {
	if err := r.validateInstanceID(req.TfInstance); err != nil {
		return nil, err
	}
	if req.BackendCompletelyDisable {
		return &LoadTFPlanReply{Message: "ok"}, nil
	}
	tf := r.terraform

	secrets := &corev1.SecretList{}
	if err := r.List(ctx, secrets, client.InNamespace(req.Namespace), client.MatchingLabels(plan.Labels(req.Name, tf.WorkspaceName(), ""))); err != nil {
		return nil, fmt.Errorf("unable to list plan secrets: %w", err)
	}
	if len(secrets.Items) == 0 {
		return nil, fmt.Errorf("no plan secrets found for plan %s", req.PendingPlan)
	}
	if !tf.Spec.Force {
		for i := range secrets.Items {
			if got := secrets.Items[i].Annotations[plan.SavedAnnotation]; got != req.PendingPlan {
				return nil, fmt.Errorf("pending plan %s does not match secret %s (%s)", req.PendingPlan, secrets.Items[i].Name, got)
			}
		}
	}

	p, err := plan.FromSecrets(req.Name, req.Namespace, string(tf.GetUID()), secrets.Items)
	if err != nil {
		return nil, fmt.Errorf("unable to reconstruct plan: %w", err)
	}
	if err := os.WriteFile(filepath.Join(r.tf.WorkingDir(), TFPlanName), p.Bytes(), 0o644); err != nil {
		return nil, fmt.Errorf("error writing plan file: %w", err)
	}
	return &LoadTFPlanReply{Message: "ok"}, nil
}
