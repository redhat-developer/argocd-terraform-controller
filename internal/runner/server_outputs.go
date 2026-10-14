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
	"reflect"

	"github.com/hashicorp/terraform-exec/tfexec"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	infrav1 "github.com/redhat-developer/argocd-terraform-controller/api/v1alpha1"
)

func (r *Server) quietOutput(ctx context.Context) (map[string]tfexec.OutputMeta, error) {
	log := r.log(ctx)
	r.tf.SetStdout(nil)
	r.tf.SetStderr(nil)
	defer r.initLogger(log)
	return r.tf.Output(ctx)
}

// Output returns the terraform outputs of the current state.
func (r *Server) Output(ctx context.Context, req *OutputRequest) (*OutputReply, error) {
	if err := r.validateInstanceID(req.TfInstance); err != nil {
		return nil, err
	}
	outputs, err := r.quietOutput(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get outputs: %w", err)
	}
	reply := &OutputReply{Outputs: make(map[string]*OutputMeta, len(outputs))}
	for k, v := range outputs {
		reply.Outputs[k] = &OutputMeta{Sensitive: v.Sensitive, Type: v.Type, Value: v.Value}
	}
	return reply, nil
}

// WriteOutputs creates or updates the outputs Secret.
func (r *Server) WriteOutputs(ctx context.Context, req *WriteOutputsRequest) (*WriteOutputsReply, error) {
	log := r.log(ctx)
	key := types.NamespacedName{Namespace: req.Namespace, Name: req.SecretName}

	var existing corev1.Secret
	err := r.Get(ctx, key, &existing)
	switch {
	case err == nil:
		if reflect.DeepEqual(existing.Data, req.Data) {
			return &WriteOutputsReply{Message: "ok", Changed: false}, nil
		}
		existing.Data = req.Data
		if err := r.Update(ctx, &existing); err != nil {
			return nil, fmt.Errorf("unable to update outputs secret: %w", err)
		}
		log.Info("updated outputs secret", "name", req.SecretName)
	case apierrors.IsNotFound(err):
		secret := corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:        req.SecretName,
				Namespace:   req.Namespace,
				Labels:      req.Labels,
				Annotations: req.Annotations,
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: infrav1.GroupVersion.String(),
					Kind:       infrav1.TerraformKind,
					Name:       req.Name,
					UID:        types.UID(req.Uuid),
					Controller: ptr.To(true),
				}},
			},
			Type: corev1.SecretTypeOpaque,
			Data: req.Data,
		}
		if err := r.Create(ctx, &secret); err != nil {
			return nil, fmt.Errorf("unable to create outputs secret: %w", err)
		}
		log.Info("created outputs secret", "name", req.SecretName)
	default:
		return nil, fmt.Errorf("unable to get outputs secret: %w", err)
	}
	return &WriteOutputsReply{Message: "ok", Changed: true}, nil
}

// GetOutputs reads the outputs Secret as strings (for health check templates).
func (r *Server) GetOutputs(ctx context.Context, req *GetOutputsRequest) (*GetOutputsReply, error) {
	var secret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: req.Namespace, Name: req.SecretName}, &secret); err != nil {
		return nil, fmt.Errorf("unable to get outputs secret: %w", err)
	}
	outputs := make(map[string]string, len(secret.Data))
	for k, v := range secret.Data {
		outputs[k] = string(v)
	}
	return &GetOutputsReply{Outputs: outputs}, nil
}
