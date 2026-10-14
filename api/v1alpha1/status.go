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

package v1alpha1

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/redhat-developer/argocd-terraform-controller/api/planid"
)

// SetTerraformReadiness sets the Ready condition, ObservedGeneration and
// LastAttemptedRevision.
func SetTerraformReadiness(tf *Terraform, status metav1.ConditionStatus, reason, message, revision string) {
	if status != metav1.ConditionUnknown {
		tf.DeleteCondition(ReconcilingCondition)
	}
	tf.markCondition(ReadyCondition, status, reason, "%s", message)
	tf.Status.ObservedGeneration = tf.Generation
	if revision != "" {
		tf.Status.LastAttemptedRevision = revision
	}
}

// TerraformProgressing marks the object as being reconciled.
func TerraformProgressing(tf *Terraform, message string) *Terraform {
	tf.MarkUnknown(ReadyCondition, ProgressingReason, "%s", message)
	tf.MarkTrue(ReconcilingCondition, ProgressingReason, "%s", message)
	return tf
}

// TerraformSuspended marks a suspended object.
func TerraformSuspended(tf *Terraform, message string) *Terraform {
	tf.DeleteCondition(ReconcilingCondition)
	tf.MarkFalse(ReadyCondition, SuspendedReason, "%s", message)
	return tf
}

// TerraformNotReady registers a failed attempt.
func TerraformNotReady(tf *Terraform, revision, reason, message string) *Terraform {
	SetTerraformReadiness(tf, metav1.ConditionFalse, reason, message, revision)
	tf.DeleteCondition(ReconcilingCondition)
	return tf
}

// TerraformApplying marks the Apply condition as in progress.
func TerraformApplying(tf *Terraform, revision, message string) *Terraform {
	tf.MarkUnknown(ConditionTypeApply, ProgressingReason, "%s", message)
	if revision != "" {
		tf.Status.LastAttemptedRevision = revision
	}
	return tf
}

// TerraformApplied records a successful apply and clears the pending plan.
func TerraformApplied(tf *Terraform, revision, message string, isDestroyApply bool, entries []ResourceRef) *Terraform {
	tf.MarkTrue(ConditionTypeApply, TFExecApplySucceedReason, "%s", message)

	if tf.Status.Plan.IsDriftDetectionPlan {
		tf.Status.LastAppliedByDriftDetectionAt = &metav1.Time{Time: time.Now()}
	}

	tf.Status.Plan = PlanStatus{
		LastApplied:   tf.Status.Plan.Pending,
		Pending:       "",
		IsDestroyPlan: isDestroyApply,
	}
	if revision != "" {
		tf.Status.LastAppliedRevision = revision
	}
	if len(entries) > 0 {
		tf.Status.Inventory = &ResourceInventory{Entries: entries}
	}

	TerraformStateLockReleased(tf)
	SetTerraformReadiness(tf, metav1.ConditionUnknown, TFExecApplySucceedReason, message+": "+revision, revision)
	return tf
}

// TerraformAppliedFailResetPlanAndNotReady records a failed apply and drops
// the pending plan so the next reconciliation re-plans.
func TerraformAppliedFailResetPlanAndNotReady(tf *Terraform, revision, reason, message string) *Terraform {
	tf.MarkFalse(ConditionTypeApply, TFExecApplyFailedReason, "%s", message)
	TerraformNotReady(tf, revision, reason, message)
	tf.Status.Plan.Pending = ""
	return tf
}

// TerraformPlannedWithChanges records a plan with changes and sets the
// pending plan ID derived from revision.
func TerraformPlannedWithChanges(tf *Terraform, revision string, forceOrAutoApply bool, message string) *Terraform {
	planID := planid.GetPlanID(revision)

	tf.MarkTrue(ConditionTypePlan, PlannedWithChangesReason, "%s", message)
	tf.Status.Plan = PlanStatus{
		LastApplied:          tf.Status.Plan.LastApplied,
		Pending:              planID,
		IsDestroyPlan:        tf.Spec.Destroy,
		IsDriftDetectionPlan: tf.HasDrift(),
	}
	if revision != "" {
		tf.Status.LastAttemptedRevision = revision
		tf.Status.LastPlannedRevision = revision
	}
	tf.Status.LastPlannedGeneration = tf.Generation
	tf.Status.LastPlanAt = &metav1.Time{Time: time.Now()}

	TerraformStateLockReleased(tf)

	switch {
	case tf.Spec.PlanOnly:
		SetTerraformReadiness(tf, metav1.ConditionUnknown, PlannedWithChangesReason, message+": This object is in plan only mode.", revision)
	case forceOrAutoApply:
		SetTerraformReadiness(tf, metav1.ConditionUnknown, PlannedWithChangesReason, message, revision)
	default:
		SetTerraformReadiness(tf, metav1.ConditionUnknown, PlannedWithChangesReason, planid.GetApproveMessage(planID, message), revision)
	}
	return tf
}

// TerraformPlannedNoChanges records a plan without changes.
func TerraformPlannedNoChanges(tf *Terraform, revision, message string) *Terraform {
	tf.MarkFalse(ConditionTypePlan, PlannedNoChangesReason, "%s", message)
	tf.Status.Plan = PlanStatus{
		LastApplied:   tf.Status.Plan.LastApplied,
		Pending:       "",
		IsDestroyPlan: tf.Spec.Destroy,
	}
	if revision != "" {
		tf.Status.LastAttemptedRevision = revision
		tf.Status.LastPlannedRevision = revision
	}
	tf.Status.LastPlannedGeneration = tf.Generation
	tf.Status.LastPlanAt = &metav1.Time{Time: time.Now()}

	TerraformStateLockReleased(tf)
	SetTerraformReadiness(tf, metav1.ConditionTrue, PlannedNoChangesReason, message+": "+revision, revision)
	return tf
}

// TerraformDriftDetected records drift.
func TerraformDriftDetected(tf *Terraform, revision, reason, message string) *Terraform {
	tf.Status.LastDriftDetectedAt = &metav1.Time{Time: time.Now()}
	TerraformStateLockReleased(tf)
	SetTerraformReadiness(tf, metav1.ConditionFalse, reason, message, revision)
	return tf
}

// TerraformNoDrift records that no drift was found.
func TerraformNoDrift(tf *Terraform, revision, reason, message string) *Terraform {
	TerraformStateLockReleased(tf)
	SetTerraformReadiness(tf, metav1.ConditionTrue, reason, message+": "+revision, revision)
	return tf
}

// TerraformOutputsAvailable records the available output names.
func TerraformOutputsAvailable(tf *Terraform, availableOutputs []string, message string) *Terraform {
	tf.MarkTrue(ConditionTypeOutput, OutputsAvailableReason, "%s", message)
	tf.Status.AvailableOutputs = availableOutputs
	return tf
}

// TerraformOutputsWritten records that outputs were written to the Secret.
func TerraformOutputsWritten(tf *Terraform, revision, message string) *Terraform {
	tf.MarkTrue(ConditionTypeOutput, OutputsWrittenReason, "%s", message)
	SetTerraformReadiness(tf, metav1.ConditionTrue, OutputsWrittenReason, message+": "+revision, revision)
	return tf
}

// TerraformHealthCheckFailed records failed health checks.
func TerraformHealthCheckFailed(tf *Terraform, message string) *Terraform {
	tf.MarkFalse(ConditionTypeHealthCheck, HealthChecksFailedReason, "%s", message)
	return tf
}

// TerraformHealthCheckSucceeded records passing health checks.
func TerraformHealthCheckSucceeded(tf *Terraform, message string) *Terraform {
	tf.MarkTrue(ConditionTypeHealthCheck, HealthChecksSucceededReason, "%s", message)
	return tf
}

// TerraformForceUnlock records a force-unlock attempt.
func TerraformForceUnlock(tf *Terraform, message string) *Terraform {
	tf.MarkFalse(ConditionTypeStateLocked, TFExecForceUnlockReason, "%s", message)
	if tf.Status.Lock.Pending != "" && tf.Status.Lock.LastApplied != tf.Status.Lock.Pending {
		tf.Status.Lock.LastApplied = tf.Status.Lock.Pending
	}
	tf.Status.Lock.Pending = ""
	return tf
}

// TerraformStateLocked records that the state lock is held by lockID.
func TerraformStateLocked(tf *Terraform, lockID, message string) *Terraform {
	tf.MarkTrue(ConditionTypeStateLocked, TFExecLockHeldReason, "%s", message)
	SetTerraformReadiness(tf, metav1.ConditionFalse, TFExecLockHeldReason, message, "")
	if tf.Status.Lock.Pending != "" && tf.Status.Lock.LastApplied != tf.Status.Lock.Pending {
		tf.Status.Lock.LastApplied = tf.Status.Lock.Pending
	}
	tf.Status.Lock.Pending = lockID
	return tf
}

// TerraformStateLockReleased clears lock tracking.
func TerraformStateLockReleased(tf *Terraform) *Terraform {
	tf.DeleteCondition(ConditionTypeStateLocked)
	tf.Status.Lock.Pending = ""
	tf.Status.Lock.LastApplied = ""
	return tf
}

// TerraformReachedLimit marks the object Stalled after exhausting retries.
func TerraformReachedLimit(tf *Terraform) *Terraform {
	tf.MarkTrue(StalledCondition, RetryLimitReachedReason, "Resource reached maximum number of retries.")
	return tf
}

// TerraformResetRetry clears the Stalled condition and the failure counter.
func TerraformResetRetry(tf *Terraform) *Terraform {
	tf.DeleteCondition(StalledCondition)
	tf.Status.ReconciliationFailures = 0
	return tf
}

// HasDrift reports whether drift was detected after the last successful apply.
func (in *Terraform) HasDrift() bool {
	c := in.GetCondition(ConditionTypeApply)
	return c != nil &&
		c.Status == metav1.ConditionTrue &&
		in.Status.LastDriftDetectedAt != nil &&
		in.Status.LastDriftDetectedAt.After(c.LastTransitionTime.Time)
}
