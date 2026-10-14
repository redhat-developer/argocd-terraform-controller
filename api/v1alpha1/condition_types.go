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
	"fmt"
	"unicode/utf8"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Condition types owned by the Terraform resource.
const (
	ReadyCondition       = "Ready"
	ReconcilingCondition = "Reconciling"
	StalledCondition     = "Stalled"

	ConditionTypeApply       = "Apply"
	ConditionTypeHealthCheck = "HealthCheck"
	ConditionTypeOutput      = "Output"
	ConditionTypePlan        = "Plan"
	ConditionTypeStateLocked = "StateLocked"
)

// OwnedConditions lists the condition types the controller manages.
var OwnedConditions = []string{
	ReconcilingCondition,
	ReadyCondition,
	StalledCondition,
	ConditionTypeApply,
	ConditionTypePlan,
	ConditionTypeHealthCheck,
	ConditionTypeOutput,
	ConditionTypeStateLocked,
}

// Condition reasons.
const (
	ProgressingReason                = "Progressing"
	SucceededReason                  = "Succeeded"
	FailedReason                     = "Failed"
	SuspendedReason                  = "Suspended"
	AccessDeniedReason               = "AccessDenied"
	RunnerQuotaExhaustedReason       = "RunnerQuotaExhausted"
	SourceFetchFailedReason          = "SourceFetchFailed"
	RetryLimitReachedReason          = "RetryLimitReached"
	DeletionBlockedByDependants      = "DeletionBlockedByDependants"
	DependencyNotReadyReason         = "DependencyNotReady"
	DriftDetectedReason              = "DriftDetected"
	DriftDetectionFailedReason       = "DriftDetectionFailed"
	HealthChecksFailedReason         = "HealthChecksFailed"
	HealthChecksSucceededReason      = "HealthChecksSucceeded"
	NoDriftReason                    = "NoDrift"
	OutputsWritingFailedReason       = "OutputsWritingFailed"
	OutputsAvailableReason           = "TerraformOutputsAvailable"
	OutputsWrittenReason             = "TerraformOutputsWritten"
	PlannedNoChangesReason           = "TerraformPlannedNoChanges"
	PlannedWithChangesReason         = "TerraformPlannedWithChanges"
	PlanApprovalDisabledReason       = "PlanApprovalDisabled"
	TFExecApplyFailedReason          = "TFExecApplyFailed"
	TFExecApplySucceedReason         = "TerraformAppliedSucceed"
	TFExecDestroyFailedReason        = "TFExecDestroyFailed"
	TFExecDestroySucceedReason       = "TerraformDestroySucceed"
	TFExecForceUnlockReason          = "ForceUnlock"
	TFExecInitFailedReason           = "TFExecInitFailed"
	TFExecLockHeldReason             = "LockHeld"
	TFExecNewFailedReason            = "TFExecNewFailed"
	TFExecOutputFailedReason         = "TFExecOutputFailed"
	TFExecPlanFailedReason           = "TFExecPlanFailed"
	TFExecPlanSucceedReason          = "TerraformPlanSucceed"
	TemplateGenerationFailedReason   = "TemplateGenerationFailed"
	VarsGenerationFailedReason       = "VarsGenerationFailed"
	WorkspaceSelectFailedReason      = "SelectWorkspaceFailed"
	ReconciliationFailureReason      = "ReconciliationFailed"
	RunnerConnectionFailedReason     = "RunnerConnectionFailed"
	BackendConfigurationFailedReason = "BackendConfigurationFailed"
)

// trimString truncates str to limit runes, appending "..." when truncated.
func trimString(str string, limit int) string {
	if limit < 3 {
		limit = 3
	}
	if utf8.RuneCountInString(str) <= limit {
		return str
	}
	runes := []rune(str)
	return string(runes[:limit]) + "..."
}

func (in *Terraform) markCondition(t string, status metav1.ConditionStatus, reason, format string, args ...any) {
	apimeta.SetStatusCondition(&in.Status.Conditions, metav1.Condition{
		Type:               t,
		Status:             status,
		Reason:             reason,
		Message:            trimString(fmt.Sprintf(format, args...), MaxConditionMessageLength),
		ObservedGeneration: in.Generation,
	})
}

// MarkTrue sets condition t to True.
func (in *Terraform) MarkTrue(t, reason, format string, args ...any) {
	in.markCondition(t, metav1.ConditionTrue, reason, format, args...)
}

// MarkFalse sets condition t to False.
func (in *Terraform) MarkFalse(t, reason, format string, args ...any) {
	in.markCondition(t, metav1.ConditionFalse, reason, format, args...)
}

// MarkUnknown sets condition t to Unknown.
func (in *Terraform) MarkUnknown(t, reason, format string, args ...any) {
	in.markCondition(t, metav1.ConditionUnknown, reason, format, args...)
}

// DeleteCondition removes condition t.
func (in *Terraform) DeleteCondition(t string) {
	apimeta.RemoveStatusCondition(&in.Status.Conditions, t)
}

// GetCondition returns condition t or nil.
func (in *Terraform) GetCondition(t string) *metav1.Condition {
	return apimeta.FindStatusCondition(in.Status.Conditions, t)
}

// IsReady reports whether the Ready condition is True.
func (in *Terraform) IsReady() bool {
	return apimeta.IsStatusConditionTrue(in.Status.Conditions, ReadyCondition)
}
