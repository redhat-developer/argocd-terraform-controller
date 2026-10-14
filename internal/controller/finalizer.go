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

package controller

import (
	"context"
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	infrav1 "github.com/redhat-developer/argocd-terraform-controller/api/v1alpha1"
	"github.com/redhat-developer/argocd-terraform-controller/internal/runner"
)

// finalize handles deletion: optional destroy, cleanup of plan/output
// objects, optional state deletion, and finalizer removal.
func (r *TerraformReconciler) finalize(ctx context.Context, p *patcher, tf *infrav1.Terraform, rc runner.RunnerClient, revision, loopID string) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	if dependants := dependantsOf(tf); len(dependants) > 0 {
		msg := fmt.Sprintf("waiting for dependants to be deleted: %v", dependants)
		log.Info(msg)
		infrav1.TerraformNotReady(tf, "", infrav1.DeletionBlockedByDependants, msg)
		return ctrl.Result{RequeueAfter: tf.GetRetryInterval()}, nil
	}

	destroyed := false
	if tf.Spec.DestroyResourcesOnDeletion {
		s, err := r.setupTerraform(ctx, p, rc, tf, revision, loopID)
		defer r.cleanupDir(ctx, rc, s)
		if err != nil {
			return ctrl.Result{RequeueAfter: tf.GetRetryInterval()}, err
		}
		// The deletion timestamp makes plan() produce a destroy plan.
		if err := r.plan(ctx, p, tf, s, rc, revision); err != nil {
			return ctrl.Result{RequeueAfter: tf.GetRetryInterval()}, err
		}
		if err := p.Patch(ctx, tf); err != nil {
			return ctrl.Result{RequeueAfter: tf.GetRetryInterval()}, err
		}
		if !nothingToDestroy(tf) {
			if err := r.apply(ctx, p, tf, s, rc, revision); err != nil {
				return ctrl.Result{RequeueAfter: tf.GetRetryInterval()}, err
			}
			if err := p.Patch(ctx, tf); err != nil {
				return ctrl.Result{RequeueAfter: tf.GetRetryInterval()}, err
			}
			log.Info("destroy on deletion completed")
		}
		destroyed = true
	}

	outputSecret := ""
	if tf.Spec.WriteOutputsToSecret != nil {
		outputSecret = tf.Spec.WriteOutputsToSecret.Name
	}
	if _, err := rc.FinalizeSecrets(ctx, &runner.FinalizeSecretsRequest{
		Namespace: tf.Namespace, Name: tf.Name, Workspace: tf.WorkspaceName(),
		HasSpecifiedOutputSecret: outputSecret != "", OutputSecretName: outputSecret,
	}); err != nil {
		if st, ok := status.FromError(err); !ok || st.Code() != codes.NotFound {
			return ctrl.Result{RequeueAfter: tf.GetRetryInterval()}, fmt.Errorf("unable to finalize plan and output objects: %w", err)
		}
	}

	if destroyed && tf.Spec.TFState != nil && tf.Spec.TFState.DeleteOnDestroy && tf.UsesKubernetesBackend() {
		reply, err := rc.DeleteState(ctx, &runner.DeleteStateRequest{
			Namespace: tf.Namespace, SecretName: tf.StateSecretName(), LeaseName: tf.StateLockLeaseName(),
		})
		if err != nil {
			return ctrl.Result{RequeueAfter: tf.GetRetryInterval()}, fmt.Errorf("unable to delete state: %w", err)
		}
		if !reply.NotFound {
			r.Eventf(tf, corev1.EventTypeNormal, infrav1.TFExecDestroySucceedReason, "Deleted state secret %s", tf.StateSecretName())
		}
	}

	// Remove our dependant finalizer from each dependency.
	dependantFinalizer := infrav1.DependencyFinalizerPrefix + tf.Name
	for _, d := range tf.Spec.DependsOn {
		key := types.NamespacedName{Namespace: d.Namespace, Name: d.Name}
		if key.Namespace == "" {
			key.Namespace = tf.Namespace
		}
		dep := &infrav1.Terraform{}
		if err := r.Get(ctx, key, dep); err != nil {
			if client.IgnoreNotFound(err) == nil {
				continue
			}
			return ctrl.Result{}, err
		}
		if controllerutil.ContainsFinalizer(dep, dependantFinalizer) {
			base := dep.DeepCopy()
			controllerutil.RemoveFinalizer(dep, dependantFinalizer)
			if err := r.Patch(ctx, dep, client.MergeFrom(base), client.FieldOwner(r.FieldManager)); err != nil {
				return ctrl.Result{}, err
			}
		}
	}

	controllerutil.RemoveFinalizer(tf, infrav1.TerraformFinalizer)
	if err := p.Patch(ctx, tf); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	return ctrl.Result{}, nil
}

func nothingToDestroy(tf *infrav1.Terraform) bool {
	c := tf.GetCondition(infrav1.ConditionTypePlan)
	return c != nil && c.Status == metav1.ConditionFalse && c.Reason == infrav1.PlannedNoChangesReason && c.Message == "No objects need to be destroyed"
}
