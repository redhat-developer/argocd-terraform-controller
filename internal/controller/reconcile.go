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
	"errors"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/redhat-developer/argocd-terraform-controller/api/planid"
	infrav1 "github.com/redhat-developer/argocd-terraform-controller/api/v1alpha1"
	"github.com/redhat-developer/argocd-terraform-controller/internal/runner"
)

const (
	tfPlanFile  = runner.TFPlanName
	tfDriftFile = "tfdrift"
)

var errDriftDetected = errors.New(infrav1.DriftDetectedReason)

// reconcile runs setup -> drift detection -> plan -> apply -> outputs ->
// health checks for the given revision.
//
//nolint:gocyclo // the state machine is intentionally linear and explicit
func (r *TerraformReconciler) reconcile(ctx context.Context, p *patcher, rc runner.RunnerClient, tf *infrav1.Terraform, revision, loopID string) (*infrav1.Terraform, error) {
	log := ctrl.LoggerFrom(ctx)
	prevReady := tf.GetCondition(infrav1.ReadyCondition)
	if prevReady != nil {
		prevReady = prevReady.DeepCopy()
	}

	s, err := r.setupTerraform(ctx, p, rc, tf, revision, loopID)
	defer r.cleanupDir(ctx, rc, s)
	if err != nil {
		return tf, err
	}

	if r.shouldDetectDrift(tf, revision) {
		driftErr := r.detectDrift(ctx, tf, s, rc, revision)
		switch {
		case driftErr == nil:
			// No drift: only repair missing outputs.
			if drifted, err := r.outputsMayBeDrifted(ctx, tf); err != nil {
				return tf, err
			} else if drifted {
				if err := r.processOutputs(ctx, p, rc, tf, s, revision); err != nil {
					return tf, err
				}
			}
			return tf, nil
		case !errors.Is(driftErr, errDriftDetected):
			return tf, driftErr
		case !r.forceOrAutoApply(tf):
			return tf, driftErr
		}
		if err := p.Patch(ctx, tf); err != nil {
			return tf, err
		}
	}

	if tf.Spec.ApprovePlan == infrav1.ApprovePlanDisableValue {
		log.Info("approvePlan is disabled; drift detection only")
		return tf, nil
	}

	if r.shouldPlan(tf) {
		if err := r.plan(ctx, p, tf, s, rc, revision); err != nil {
			return tf, err
		}
		if err := p.Patch(ctx, tf); err != nil {
			return tf, err
		}
	}

	if r.shouldApply(tf) {
		// Re-plan before a manual apply so the approved plan is still current.
		if !r.forceOrAutoApply(tf) && tf.Spec.ApprovePlan != "" {
			approved := tf.Status.Plan.Pending
			if err := r.plan(ctx, p, tf, s, rc, revision); err != nil {
				return tf, err
			}
			if err := p.Patch(ctx, tf); err != nil {
				return tf, err
			}
			if tf.Status.Plan.Pending == "" || tf.Status.Plan.Pending != approved {
				log.Info("plan changed while waiting for approval", "approved", approved, "pending", tf.Status.Plan.Pending)
				return tf, nil
			}
		}
		if err := r.apply(ctx, p, tf, s, rc, revision); err != nil {
			return tf, err
		}
		if err := p.Patch(ctx, tf); err != nil {
			return tf, err
		}
	}

	if err := r.processOutputs(ctx, p, rc, tf, s, revision); err != nil {
		return tf, err
	}

	if r.shouldDoHealthChecks(tf) {
		if err := r.doHealthChecks(ctx, tf, rc); err != nil {
			return tf, err
		}
		if err := p.Patch(ctx, tf); err != nil {
			return tf, err
		}
	}

	if tf.Spec.GetEnableInventory() && tf.Status.Inventory == nil {
		if entries, err := r.inventory(ctx, rc, s); err == nil && len(entries) > 0 {
			tf.Status.Inventory = &infrav1.ResourceInventory{Entries: entries}
		}
	} else if !tf.Spec.GetEnableInventory() {
		tf.Status.Inventory = nil
	}

	// Settle an Unknown Ready condition that is not an approval prompt.
	if c := tf.GetCondition(infrav1.ReadyCondition); c != nil && c.Status == metav1.ConditionUnknown {
		awaitingApproval := c.Reason == infrav1.PlannedWithChangesReason && strings.HasPrefix(c.Message, "Plan generated")
		switch {
		case awaitingApproval:
		case c.Reason != infrav1.ProgressingReason:
			tf.MarkTrue(infrav1.ReadyCondition, c.Reason, "%s", c.Message)
			tf.DeleteCondition(infrav1.ReconcilingCondition)
		case prevReady != nil && prevReady.Reason != infrav1.ProgressingReason:
			// Nothing happened this run (e.g. a pending plan in planOnly mode);
			// keep the previous, more informative condition.
			infrav1.SetTerraformReadiness(tf, prevReady.Status, prevReady.Reason, prevReady.Message, "")
			tf.DeleteCondition(infrav1.ReconcilingCondition)
		}
	}
	return tf, nil
}

func (r *TerraformReconciler) cleanupDir(ctx context.Context, rc runner.RunnerClient, s *session) {
	if s == nil || s.tmpDir == "" {
		return
	}
	if _, err := rc.CleanupDir(context.WithoutCancel(ctx), &runner.CleanupDirRequest{TmpDir: s.tmpDir}); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "unable to clean up runner working directory")
	}
}

func (r *TerraformReconciler) inventory(ctx context.Context, rc runner.RunnerClient, s *session) ([]infrav1.ResourceRef, error) {
	reply, err := rc.GetInventory(ctx, &runner.GetInventoryRequest{TfInstance: s.instance})
	if err != nil {
		return nil, err
	}
	entries := make([]infrav1.ResourceRef, 0, len(reply.Inventories))
	for _, inv := range reply.Inventories {
		entries = append(entries, infrav1.ResourceRef{Name: inv.Name, Type: inv.Type, Identifier: inv.Identifier})
	}
	return entries, nil
}

// --- drift detection ---

func (r *TerraformReconciler) shouldDetectDrift(tf *infrav1.Terraform, revision string) bool {
	if tf.Spec.DisableDriftDetection || tf.Spec.Destroy {
		return false
	}
	if tf.Spec.ApprovePlan == infrav1.ApprovePlanDisableValue {
		return true
	}
	st := tf.Status
	if st.LastAppliedRevision == "" && st.LastPlannedRevision == "" && st.LastAttemptedRevision == "" {
		return false
	}
	if tf.Generation != st.ObservedGeneration {
		return false
	}
	if st.LastAttemptedRevision == st.LastAppliedRevision && st.LastAttemptedRevision == st.LastPlannedRevision &&
		st.LastAttemptedRevision == revision && st.Plan.Pending == "" {
		return true
	}
	if st.LastAttemptedRevision == st.LastPlannedRevision && st.LastAttemptedRevision == revision && st.Plan.Pending == "" {
		return true
	}
	return false
}

func (r *TerraformReconciler) detectDrift(ctx context.Context, tf *infrav1.Terraform, s *session, rc runner.RunnerClient, revision string) error {
	log := ctrl.LoggerFrom(ctx)
	req := &runner.PlanRequest{TfInstance: s.instance, Out: tfDriftFile, Refresh: true, Targets: tf.Spec.Targets, SourceRefRootDir: s.tmpDir}
	if backendCompletelyDisabled(tf) {
		req.Out = ""
	}
	reply, err := rc.Plan(ctx, req)
	if err != nil {
		if id, ok := lockDetail(err); ok {
			r.Eventf(tf, corev1.EventTypeWarning, infrav1.DriftDetectionFailedReason, "Drift detection error: State locked with Lock Identifier %s", id)
			infrav1.TerraformStateLocked(tf, id, fmt.Sprintf("Terraform Locked with Lock Identifier: %s", id))
		} else {
			r.Eventf(tf, corev1.EventTypeWarning, infrav1.DriftDetectionFailedReason, "Drift detection error: %s", err)
		}
		infrav1.TerraformNotReady(tf, revision, infrav1.DriftDetectionFailedReason, fmt.Sprintf("error running plan: %s", err))
		return fmt.Errorf("error running plan: %w", err)
	}
	if !reply.Drifted {
		infrav1.TerraformNoDrift(tf, revision, infrav1.NoDriftReason, "No drift")
		return nil
	}

	raw := "not available"
	if req.Out != "" {
		show, err := rc.ShowPlanFileRaw(ctx, &runner.ShowPlanFileRawRequest{TfInstance: s.instance, Filename: tfDriftFile})
		if err != nil {
			infrav1.TerraformNotReady(tf, revision, infrav1.DriftDetectionFailedReason, err.Error())
			return err
		}
		raw = strings.Replace(show.RawOutput, "You can apply this plan to save these new output values to the Terraform\nstate, without changing any real infrastructure.", "", 1)
	}
	log.Info("drift detected")
	r.Eventf(tf, corev1.EventTypeWarning, infrav1.DriftDetectedReason, "Drift detected.\n%s", raw)
	infrav1.TerraformDriftDetected(tf, revision, infrav1.DriftDetectedReason, raw)
	return errDriftDetected
}

// --- plan ---

func (r *TerraformReconciler) shouldPlan(tf *infrav1.Terraform) bool {
	return tf.Spec.Force || tf.Status.Plan.Pending == ""
}

func (r *TerraformReconciler) plan(ctx context.Context, p *patcher, tf *infrav1.Terraform, s *session, rc runner.RunnerClient, revision string) error {
	log := ctrl.LoggerFrom(ctx)
	infrav1.TerraformProgressing(tf, "Terraform Planning")
	if err := p.Patch(ctx, tf); err != nil {
		return err
	}

	req := &runner.PlanRequest{TfInstance: s.instance, Out: tfPlanFile, Refresh: true, Targets: tf.Spec.Targets, SourceRefRootDir: s.tmpDir}
	if backendCompletelyDisabled(tf) {
		req.Out = ""
	}
	if tf.Spec.Destroy || (isBeingDeleted(tf) && tf.Spec.DestroyResourcesOnDeletion) {
		log.Info("planning a destroy")
		req.Destroy = true
	}
	if tf.Spec.TFState != nil && tf.Spec.TFState.LockTimeout.Duration > 0 {
		req.LockTimeout = tf.Spec.TFState.LockTimeout.Duration.String()
	}

	reply, err := rc.Plan(ctx, req)
	if err != nil {
		if id, ok := lockDetail(err); ok {
			r.Eventf(tf, corev1.EventTypeWarning, infrav1.TFExecPlanFailedReason, "Plan error: State locked with Lock Identifier %s", id)
			infrav1.TerraformStateLocked(tf, id, fmt.Sprintf("Terraform Locked with Lock Identifier: %s", id))
		} else {
			r.Eventf(tf, corev1.EventTypeWarning, infrav1.TFExecPlanFailedReason, "Plan error: %s", err)
		}
		infrav1.TerraformNotReady(tf, revision, infrav1.TFExecPlanFailedReason, fmt.Sprintf("error running plan: %s", err))
		return fmt.Errorf("error running plan: %w", err)
	}

	if req.Destroy && !reply.PlanCreated {
		infrav1.TerraformPlannedNoChanges(tf, revision, "No objects need to be destroyed")
		return nil
	}

	save, err := rc.SaveTFPlan(ctx, &runner.SaveTFPlanRequest{
		TfInstance: s.instance, BackendCompletelyDisable: backendCompletelyDisabled(tf),
		Name: tf.Name, Namespace: tf.Namespace, Uuid: string(tf.UID), Revision: revision,
	})
	if err != nil {
		infrav1.TerraformNotReady(tf, revision, infrav1.TFExecPlanFailedReason, fmt.Sprintf("error saving plan: %s", err))
		return fmt.Errorf("error saving plan: %w", err)
	}
	tf.Status.PlanConfigMapName = ""
	if tf.Spec.StoreReadablePlan == infrav1.StoreReadablePlanHuman {
		tf.Status.PlanConfigMapName = save.ReadablePlanName
	}

	if !reply.Drifted {
		infrav1.TerraformPlannedNoChanges(tf, revision, "Plan no changes")
		return nil
	}
	forceOrAuto := r.forceOrAutoApply(tf)
	if !forceOrAuto {
		msg := planid.GetApproveMessage(planid.GetPlanID(revision), "Plan generated")
		r.Eventf(tf, corev1.EventTypeNormal, infrav1.TFExecPlanSucceedReason, "Planned.\n%s", msg)
	}
	infrav1.TerraformPlannedWithChanges(tf, revision, forceOrAuto, "Plan generated")
	return nil
}

// --- apply ---

func (r *TerraformReconciler) forceOrAutoApply(tf *infrav1.Terraform) bool {
	return tf.Spec.Force || tf.Spec.ApprovePlan == infrav1.ApprovePlanAutoValue
}

func (r *TerraformReconciler) shouldApply(tf *infrav1.Terraform) bool {
	if tf.Spec.Force {
		return true
	}
	if tf.Spec.PlanOnly || tf.Spec.ApprovePlan == "" || tf.Spec.ApprovePlan == infrav1.ApprovePlanDisableValue {
		return false
	}
	if tf.Spec.ApprovePlan == infrav1.ApprovePlanAutoValue {
		return tf.Status.Plan.Pending != ""
	}
	return planid.Matches(tf.Spec.ApprovePlan, tf.Status.Plan.Pending)
}

func (r *TerraformReconciler) apply(ctx context.Context, p *patcher, tf *infrav1.Terraform, s *session, rc runner.RunnerClient, revision string) error {
	log := ctrl.LoggerFrom(ctx)
	infrav1.TerraformProgressing(tf, "Applying")
	if err := p.Patch(ctx, tf); err != nil {
		return err
	}

	disabled := backendCompletelyDisabled(tf)
	if _, err := rc.LoadTFPlan(ctx, &runner.LoadTFPlanRequest{
		TfInstance: s.instance, Name: tf.Name, Namespace: tf.Namespace, BackendCompletelyDisable: disabled, PendingPlan: tf.Status.Plan.Pending,
	}); err != nil {
		// Force a re-plan on the next run.
		tf.Status.Plan.Pending = ""
		tf.Status.LastPlannedRevision = ""
		tf.Status.LastAttemptedRevision = ""
		infrav1.TerraformNotReady(tf, revision, infrav1.TFExecApplyFailedReason, err.Error())
		return err
	}

	infrav1.TerraformApplying(tf, revision, "Apply started")
	if err := p.Patch(ctx, tf); err != nil {
		return err
	}

	var (
		isDestroy bool
		entries   []infrav1.ResourceRef
	)
	failed := func(kind, reason string, err error) error {
		if id, ok := lockDetail(err); ok {
			r.Eventf(tf, corev1.EventTypeWarning, reason, "%s error: State locked with Lock Identifier %s", kind, id)
			infrav1.TerraformStateLocked(tf, id, fmt.Sprintf("Terraform Locked with Lock Identifier: %s", id))
		} else {
			r.Eventf(tf, corev1.EventTypeWarning, reason, "%s error: %s", kind, err)
		}
		msg := fmt.Sprintf("error running %s: %s", strings.ToLower(kind), err)
		infrav1.TerraformAppliedFailResetPlanAndNotReady(tf, revision, infrav1.TFExecApplyFailedReason, msg)
		return errors.New(msg)
	}

	if disabled && tf.Spec.Destroy {
		// Without a backend there is no plan file; destroy directly.
		if _, err := rc.Destroy(ctx, &runner.DestroyRequest{TfInstance: s.instance, Targets: tf.Spec.Targets}); err != nil {
			return failed("Destroy", infrav1.TFExecDestroyFailedReason, err)
		}
		isDestroy = true
	} else {
		req := &runner.ApplyRequest{TfInstance: s.instance, Parallelism: tf.Spec.Parallelism, RefreshBeforeApply: tf.Spec.RefreshBeforeApply, Targets: tf.Spec.Targets}
		if !disabled {
			req.DirOrPlan = tfPlanFile
		}
		if _, err := rc.Apply(ctx, req); err != nil {
			return failed("Apply", infrav1.TFExecApplyFailedReason, err)
		}
		isDestroy = tf.Status.Plan.IsDestroyPlan
		if tf.Spec.GetEnableInventory() && !isDestroy {
			var err error
			if entries, err = r.inventory(ctx, rc, s); err != nil {
				return failed("Inventory", infrav1.TFExecApplyFailedReason, err)
			}
		} else if !tf.Spec.GetEnableInventory() {
			tf.Status.Inventory = nil
		}
	}

	msg := "Applied successfully"
	reason := infrav1.TFExecApplySucceedReason
	if isDestroy {
		msg, reason = "Destroy applied successfully", infrav1.TFExecDestroySucceedReason
	}
	log.Info(msg)
	r.Eventf(tf, corev1.EventTypeNormal, reason, "%s", msg)
	infrav1.TerraformApplied(tf, revision, msg, isDestroy, entries)
	return nil
}
