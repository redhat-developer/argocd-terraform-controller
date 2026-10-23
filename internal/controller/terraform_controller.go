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

// Package controller reconciles Terraform objects by fetching their source,
// driving a runner Pod over gRPC through init/plan/apply, and recording the
// outcome in the object's status.
package controller

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	infrav1 "github.com/redhat-developer/argocd-terraform-controller/api/v1alpha1"
	"github.com/redhat-developer/argocd-terraform-controller/internal/mtls"
	"github.com/redhat-developer/argocd-terraform-controller/internal/source"
)

// ReconcileRequestAnnotation triggers an immediate reconciliation when its
// value changes (set it to a timestamp).
const ReconcileRequestAnnotation = "infra.argocd.io/reconcile"

// TerraformReconciler reconciles Terraform objects.
type TerraformReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	Recorder    record.EventRecorder
	CertRotator *mtls.CertRotator
	Source      *source.Manager

	// RunnerImage is the default runner Pod image.
	RunnerImage string
	// RunnerGRPCPort is the port the runner listens on.
	RunnerGRPCPort int
	// RunnerGRPCMaxMessageSize in MiB.
	RunnerGRPCMaxMessageSize int
	// RunnerCreationTimeout bounds waiting for the runner Pod to get an IP.
	RunnerCreationTimeout time.Duration
	// RunnerRPCTimeout bounds the setup phase RPCs.
	RunnerRPCTimeout time.Duration
	// ClusterDomain is used to build runner Pod DNS names.
	ClusterDomain string
	// NoCrossNamespaceRefs denies dependsOn and cliConfigSecretRef
	// references to other namespaces.
	NoCrossNamespaceRefs bool
	FieldManager         string
}

// +kubebuilder:rbac:groups=infra.argocd.io,resources=terraforms,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=infra.argocd.io,resources=terraforms/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=infra.argocd.io,resources=terraforms/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets;configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete

// Eventf records an event on tf.
func (r *TerraformReconciler) Eventf(tf *infrav1.Terraform, eventType, reason, format string, args ...any) {
	if r.Recorder != nil {
		r.Recorder.Eventf(tf, eventType, reason, format, args...)
	}
}

// patcher persists changes to a Terraform object as merge patches against
// the last persisted state (metadata/finalizers first, then status).
type patcher struct {
	client client.Client
	before *infrav1.Terraform
}

func newPatcher(c client.Client, tf *infrav1.Terraform) *patcher {
	return &patcher{client: c, before: tf.DeepCopy()}
}

// Patch writes tf and refreshes the baseline.
func (p *patcher) Patch(ctx context.Context, tf *infrav1.Terraform) error {
	before := p.before
	metaChanged := !equalStrings(before.Finalizers, tf.Finalizers) || !equalMaps(before.Annotations, tf.Annotations) || !equalMaps(before.Labels, tf.Labels)

	status := tf.Status
	if metaChanged {
		if err := p.client.Patch(ctx, tf, client.MergeFrom(before)); err != nil {
			return err
		}
		// The response overwrote status with the server copy; restore ours.
		tf.Status = status
	}
	if err := p.client.Status().Patch(ctx, tf, client.MergeFrom(before)); err != nil {
		return err
	}
	p.before = tf.DeepCopy()
	return nil
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalMaps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func isBeingDeleted(tf *infrav1.Terraform) bool { return !tf.DeletionTimestamp.IsZero() }

// SetupWithManager registers the controller.
func (r *TerraformReconciler) SetupWithManager(mgr ctrl.Manager, maxConcurrentReconciles int) error {
	if r.FieldManager == "" {
		r.FieldManager = "argocd-terraform-controller"
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&infrav1.Terraform{}, builder.WithPredicates(builderPredicates())).
		WithOptions(controller.Options{MaxConcurrentReconciles: maxConcurrentReconciles}).
		Named("terraform").
		Complete(r)
}

// builderPredicates ignores status-only updates; both predicates pass
// create and delete events through.
func builderPredicates() predicate.Predicate {
	return predicate.Or(
		predicate.GenerationChangedPredicate{},
		predicate.AnnotationChangedPredicate{},
	)
}

// Reconcile drives one reconciliation of a Terraform object.
//
//nolint:gocyclo // the reconciliation flow is intentionally linear and explicit
func (r *TerraformReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, retErr error) {
	start := time.Now()
	loopID := uuid.New().String()
	log := ctrl.LoggerFrom(ctx).WithValues("loop-id", loopID)
	ctx = ctrl.LoggerInto(ctx, log)

	if r.CertRotator != nil && r.CertRotator.Ready != nil {
		select {
		case <-r.CertRotator.Ready:
		case <-ctx.Done():
			return ctrl.Result{}, ctx.Err()
		}
	}

	tf := &infrav1.Terraform{}
	if err := r.Get(ctx, req.NamespacedName, tf); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	log.Info("reconciling", "generation", tf.Generation)
	patchHelper := newPatcher(r.Client, tf)

	defer func() {
		if v, ok := tf.Annotations[ReconcileRequestAnnotation]; ok {
			tf.Status.LastHandledReconcileAt = v
		}
		if tf.IsReady() {
			tf.Status.ObservedGeneration = tf.Generation
		}
		if err := patchHelper.Patch(ctx, tf); err != nil {
			if isBeingDeleted(tf) && apierrors.IsNotFound(err) {
				return
			}
			retErr = errors.Join(retErr, err)
		}
	}()

	if !controllerutil.ContainsFinalizer(tf, infrav1.TerraformFinalizer) {
		controllerutil.AddFinalizer(tf, infrav1.TerraformFinalizer)
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}

	if tf.Spec.Suspend && !isBeingDeleted(tf) {
		log.Info("reconciliation is suspended")
		infrav1.TerraformSuspended(tf, "Reconciliation is suspended")
		return ctrl.Result{}, nil
	}

	tf.MarkTrue(infrav1.ReconcilingCondition, infrav1.ProgressingReason, "Fulfilling prerequisites")
	if err := patchHelper.Patch(ctx, tf); err != nil {
		return ctrl.Result{}, err
	}

	// Deletion blocked by dependants.
	if isBeingDeleted(tf) {
		if dependants := dependantsOf(tf); len(dependants) > 0 {
			msg := fmt.Sprintf("Deletion in progress, but blocked. Please delete %s to resume.", strings.Join(dependants, ", "))
			infrav1.TerraformNotReady(tf, "", infrav1.DeletionBlockedByDependants, msg)
			return ctrl.Result{RequeueAfter: tf.GetRetryInterval()}, nil
		}
	}

	// Resolve the source revision.
	revision, err := r.Source.Resolve(ctx, tf)
	if err != nil {
		if _, ok := errors.AsType[*source.NotFoundError](err); ok {
			tf.MarkTrue(infrav1.StalledCondition, infrav1.SourceFetchFailedReason, "%s", err)
			infrav1.TerraformNotReady(tf, "", infrav1.SourceFetchFailedReason, err.Error())
			r.Eventf(tf, corev1.EventTypeWarning, infrav1.SourceFetchFailedReason, "%s", err.Error())
			return ctrl.Result{RequeueAfter: tf.GetRetryInterval()}, nil
		}
		if isBeingDeleted(tf) && !tf.Spec.DestroyResourcesOnDeletion {
			// Nothing to destroy; the source is not needed to finalize.
			revision = tf.Status.LastAttemptedRevision
		} else {
			infrav1.TerraformNotReady(tf, "", infrav1.SourceFetchFailedReason, fmt.Sprintf("could not resolve source: %s", err))
			r.Eventf(tf, corev1.EventTypeWarning, infrav1.SourceFetchFailedReason, "%s", err.Error())
			return ctrl.Result{RequeueAfter: tf.GetRetryInterval()}, nil
		}
	}
	tf.DeleteCondition(infrav1.StalledCondition)
	if c := tf.GetCondition(infrav1.ReadyCondition); c != nil && c.Reason == infrav1.SourceFetchFailedReason {
		tf.MarkUnknown(infrav1.ReadyCondition, infrav1.ProgressingReason, "Reconciliation in progress")
	}

	shouldReconcile, reason, requeueAfter := r.shouldReconcile(tf, revision)
	if !shouldReconcile {
		log.Info("skipping reconciliation", "reason", reason, "requeueAfter", requeueAfter)
		tf.DeleteCondition(infrav1.ReconcilingCondition)
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}
	log.Info("proceeding", "reason", reason, "revision", revision)

	if len(tf.Spec.DependsOn) > 0 && !isBeingDeleted(tf) {
		if err := r.checkDependencies(ctx, tf, revision); err != nil {
			if _, ok := errors.AsType[*accessDeniedError](err); ok {
				infrav1.TerraformNotReady(tf, revision, infrav1.AccessDeniedReason, err.Error())
				return ctrl.Result{}, nil
			}
			infrav1.TerraformNotReady(tf, revision, infrav1.DependencyNotReadyReason, err.Error())
			msg := fmt.Sprintf("Dependencies do not meet ready condition, retrying in %s", tf.GetRetryInterval())
			log.Info(msg)
			r.Eventf(tf, corev1.EventTypeNormal, infrav1.DependencyNotReadyReason, "%s", msg)
			return ctrl.Result{RequeueAfter: tf.GetRetryInterval()}, nil
		}
	}
	if c := tf.GetCondition(infrav1.ReadyCondition); c != nil && (c.Reason == infrav1.AccessDeniedReason || c.Reason == infrav1.DependencyNotReadyReason) {
		tf.MarkUnknown(infrav1.ReadyCondition, infrav1.ProgressingReason, "Reconciliation in progress")
	}

	// Remember the outcome of the previous run before marking this one as
	// in progress; clearStalePendingPlan uses it.
	previousReason := readyReason(tf)

	// Keep an Unknown Ready condition (e.g. the approval prompt) visible.
	if ready := tf.GetCondition(infrav1.ReadyCondition); ready == nil || ready.Status != metav1.ConditionUnknown {
		msg := "Reconciliation in progress"
		if isBeingDeleted(tf) {
			msg = "Deletion in progress"
		}
		infrav1.TerraformProgressing(tf, msg)
		if err := patchHelper.Patch(ctx, tf); err != nil {
			return ctrl.Result{}, err
		}
	}

	if revision != tf.Status.LastAttemptedRevision || tf.Generation != tf.Status.ObservedGeneration {
		infrav1.TerraformResetRetry(tf)
	}

	if !isBeingDeleted(tf) {
		r.clearStalePendingPlan(ctx, tf, revision, previousReason)
		if tf.Status.Plan.Pending != "" && !r.forceOrAutoApply(tf) && !r.shouldApply(tf) {
			log.Info("waiting for manual approval", "pendingPlan", tf.Status.Plan.Pending)
			tf.DeleteCondition(infrav1.ReconcilingCondition)
			return ctrl.Result{RequeueAfter: r.sourcePollInterval(tf)}, nil
		}
	}

	runnerClient, closeConn, err := r.LookupOrCreateRunner(ctx, tf, revision)
	if err != nil {
		if closeConn != nil {
			_ = closeConn()
		}
		if isQuotaError(err) {
			delay := wait.Jitter(tf.GetRetryInterval(), 0.5)
			r.Eventf(tf, corev1.EventTypeWarning, infrav1.RunnerQuotaExhaustedReason, "runner pod creation blocked by resource quota, retrying in %s", delay)
			infrav1.TerraformNotReady(tf, revision, infrav1.RunnerQuotaExhaustedReason, err.Error())
			return ctrl.Result{RequeueAfter: delay}, nil
		}
		infrav1.TerraformNotReady(tf, revision, infrav1.RunnerConnectionFailedReason, err.Error())
		return ctrl.Result{}, err
	}
	defer r.cleanupRunner(ctx, tf, closeConn)

	if isBeingDeleted(tf) {
		res, err := r.finalize(ctx, patchHelper, tf, runnerClient, revision, loopID)
		return res, err
	}

	if !tf.ShouldRetry() {
		log.Info("resource reached maximum number of retries", "failures", tf.Status.ReconciliationFailures, "retries", tf.GetRetries())
		infrav1.TerraformReachedLimit(tf)
		tf.DeleteCondition(infrav1.ReconcilingCondition)
		return ctrl.Result{}, nil
	}

	reconciled, reconcileErr := r.reconcile(ctx, patchHelper, runnerClient, tf.DeepCopy(), revision, loopID)
	*tf = *reconciled
	driftDetected := reconcileErr != nil && reconcileErr.Error() == infrav1.DriftDetectedReason
	switch {
	case reconcileErr == nil:
		infrav1.TerraformResetRetry(tf)
		tf.Status.LastSuccessfulReconcileAt = &metav1.Time{Time: time.Now()}
	case driftDetected:
		// Drift is a finding, not a failure: the run completed and observed
		// that the infrastructure differs. Do not count it against
		// remediation.retries or shorten the schedule to retryInterval.
		infrav1.TerraformResetRetry(tf)
	default:
		tf.IncrementReconciliationFailures()
	}

	switch {
	case driftDetected:
		next := r.nextInterval(tf)
		log.Info("drift detected", "elapsed", time.Since(start), "nextCheck", next)
		return ctrl.Result{RequeueAfter: next}, nil
	case reconcileErr != nil:
		log.Error(reconcileErr, "reconciliation failed", "elapsed", time.Since(start), "nextTry", tf.GetRetryInterval())
		r.Eventf(tf, corev1.EventTypeWarning, infrav1.ReconciliationFailureReason, "%s", reconcileErr.Error())
		return ctrl.Result{RequeueAfter: tf.GetRetryInterval()}, nil
	}

	log.Info("reconciliation completed", "elapsed", time.Since(start))
	if tf.Status.Plan.Pending != "" && !r.forceOrAutoApply(tf) {
		log.Info("waiting for manual approval")
		return ctrl.Result{RequeueAfter: r.sourcePollInterval(tf)}, nil
	}
	return ctrl.Result{RequeueAfter: r.nextInterval(tf)}, nil
}

// sourcePollInterval is how often the source is checked for new revisions.
func (r *TerraformReconciler) sourcePollInterval(tf *infrav1.Terraform) time.Duration {
	if tf.Spec.Source.Interval != nil && tf.Spec.Source.Interval.Duration > 0 {
		return tf.Spec.Source.Interval.Duration
	}
	return tf.Spec.Interval.Duration
}

func (r *TerraformReconciler) nextInterval(tf *infrav1.Terraform) time.Duration {
	return min(tf.Spec.Interval.Duration, r.sourcePollInterval(tf))
}

// clearStalePendingPlan drops a pending plan that no longer matches the
// desired state so the next step re-plans.
func (r *TerraformReconciler) clearStalePendingPlan(ctx context.Context, tf *infrav1.Terraform, revision, previousReason string) {
	log := ctrl.LoggerFrom(ctx)
	revisionChanged := revision != tf.Status.LastAttemptedRevision
	requestedAt, hasRequest := tf.Annotations[ReconcileRequestAnnotation]
	newRequest := hasRequest && requestedAt != tf.Status.LastHandledReconcileAt
	switch {
	case newRequest && tf.Status.Plan.Pending != "" && !r.shouldApply(tf):
		// A manual reconcile request while a plan awaits approval means "re-plan".
		log.Info("reconcile requested with an unapproved pending plan, clearing it to re-plan")
		tf.Status.Plan.Pending = ""
		// Forget the last plan so the next run plans instead of detecting drift.
		tf.Status.LastPlannedRevision = ""
	case newRequest && tf.Status.Plan.Pending == "" && previousReason == infrav1.DriftDetectedReason:
		// A manual reconcile request after drift was detected means "plan the
		// remediation": forget the last plan so the run plans (producing an
		// approvable plan) instead of only detecting the same drift again.
		log.Info("reconcile requested after drift was detected, planning the remediation")
		tf.Status.LastPlannedRevision = ""
	case revisionChanged && !r.shouldApply(tf) && strings.HasPrefix(tf.Spec.ApprovePlan, "replan") &&
		strings.HasPrefix("re"+tf.Status.Plan.Pending, tf.Spec.ApprovePlan):
		log.Info("replan requested, clearing pending plan")
		tf.Status.Plan.Pending = ""
	case revisionChanged && tf.Spec.PlanOnly:
		tf.Status.Plan.Pending = ""
	case revisionChanged && tf.Status.Plan.Pending != "" && !r.shouldApply(tf):
		log.Info("source changed while a plan was pending, clearing pending plan")
		tf.Status.Plan.Pending = ""
	case tf.Status.LastPlannedGeneration != 0 && tf.Generation != tf.Status.LastPlannedGeneration &&
		tf.Status.Plan.Pending != "" && !r.shouldApply(tf):
		log.Info("spec changed while a plan was pending, clearing pending plan")
		tf.Status.Plan.Pending = ""
	}
}

// shouldReconcile decides whether to run now or requeue later.
func (r *TerraformReconciler) shouldReconcile(tf *infrav1.Terraform, revision string) (bool, string, time.Duration) {
	if tf.Spec.Force {
		return true, "force is enabled", 0
	}
	if isBeingDeleted(tf) {
		return true, "object is being deleted", 0
	}
	if v, ok := tf.Annotations[ReconcileRequestAnnotation]; ok && tf.Status.LastHandledReconcileAt != v {
		return true, "new reconcile request annotation", 0
	}
	if tf.Status.LastPlanAt == nil {
		return true, "never planned before", 0
	}
	if tf.Status.ObservedGeneration != 0 && tf.Generation != tf.Status.ObservedGeneration {
		return true, "generation has changed", 0
	}
	if r.shouldApply(tf) {
		return true, "apply should run", 0
	}
	if revision != "" && tf.Status.LastAttemptedRevision != revision {
		return true, "source revision has changed", 0
	}
	if c := tf.GetCondition(infrav1.ReadyCondition); c != nil && c.Reason == infrav1.DependencyNotReadyReason {
		return true, "previously blocked on a dependency", 0
	}
	// Detected drift waits for the regular interval, not the retry interval:
	// re-checking sooner cannot change the outcome until a human (manual
	// mode) or a new revision acts on it.
	if readyReason(tf) == infrav1.DriftDetectedReason && tf.Status.LastDriftDetectedAt != nil {
		if in := time.Until(tf.Status.LastDriftDetectedAt.Add(tf.Spec.Interval.Duration)); in > 0 {
			return false, "drift detected, interval has not elapsed", min(in, r.sourcePollInterval(tf))
		}
		return true, "drift detected, interval elapsed", 0
	}
	if tf.Status.ReconciliationFailures > 0 && !tf.IsReady() {
		if retryIn := time.Until(tf.Status.LastPlanAt.Add(tf.GetRetryInterval())); retryIn > 0 {
			return false, "retry interval has not elapsed", retryIn
		}
		return true, "retry interval elapsed", 0
	}
	if tf.Status.LastSuccessfulReconcileAt == nil {
		return true, "never successfully reconciled", 0
	}
	if in := time.Until(tf.Status.LastSuccessfulReconcileAt.Add(tf.Spec.Interval.Duration)); in > 0 {
		return false, "interval has not elapsed", min(in, r.sourcePollInterval(tf))
	}
	return true, "interval elapsed", 0
}

// accessDeniedError marks a cross-namespace reference denied by policy.
type accessDeniedError struct{ msg string }

func (e *accessDeniedError) Error() string { return e.msg }

// readyReason returns the reason of the Ready condition, or "" when unset.
func readyReason(tf *infrav1.Terraform) string {
	if c := tf.GetCondition(infrav1.ReadyCondition); c != nil {
		return c.Reason
	}
	return ""
}

func dependantsOf(tf *infrav1.Terraform) []string {
	var out []string
	for _, f := range tf.Finalizers {
		if after, ok := strings.CutPrefix(f, infrav1.DependencyFinalizerPrefix); ok {
			out = append(out, after)
		}
	}
	return out
}

// checkDependencies verifies spec.dependsOn and adds a dependant finalizer to
// each dependency so it cannot be deleted while this object exists.
func (r *TerraformReconciler) checkDependencies(ctx context.Context, tf *infrav1.Terraform, revision string) error {
	finalizer := infrav1.DependencyFinalizerPrefix + tf.Name
	for _, d := range tf.Spec.DependsOn {
		key := types.NamespacedName{Namespace: d.Namespace, Name: d.Name}
		if key.Namespace == "" {
			key.Namespace = tf.Namespace
		}
		if r.NoCrossNamespaceRefs && key.Namespace != tf.Namespace {
			return &accessDeniedError{msg: fmt.Sprintf("cannot access %s, cross-namespace references have been disabled", key)}
		}
		dep := &infrav1.Terraform{}
		if err := r.Get(ctx, key, dep); err != nil {
			return fmt.Errorf("unable to get dependency %s: %w", key, err)
		}
		if dep.DeletionTimestamp.IsZero() && !controllerutil.ContainsFinalizer(dep, finalizer) {
			base := dep.DeepCopy()
			controllerutil.AddFinalizer(dep, finalizer)
			if err := r.Patch(ctx, dep, client.MergeFrom(base), client.FieldOwner(r.FieldManager)); err != nil {
				return fmt.Errorf("unable to add finalizer to dependency %s: %w", key, err)
			}
		}
		if dep.Generation != dep.Status.ObservedGeneration || !dep.IsReady() {
			return fmt.Errorf("dependency %s is not ready", key)
		}
		if sameSource(dep, tf) && revision != dep.Status.LastAppliedRevision && revision != dep.Status.LastPlannedRevision {
			return fmt.Errorf("dependency %s is not updated yet", key)
		}
		if dep.Spec.WriteOutputsToSecret != nil {
			secretKey := types.NamespacedName{Namespace: dep.Namespace, Name: dep.Spec.WriteOutputsToSecret.Name}
			if err := r.Get(ctx, secretKey, &corev1.Secret{}); err != nil {
				return fmt.Errorf("dependency output secret %s of %s is not ready yet", secretKey, key)
			}
		}
	}
	return nil
}

func sameSource(a, b *infrav1.Terraform) bool {
	switch {
	case a.Spec.Source.Git != nil && b.Spec.Source.Git != nil:
		return a.Spec.Source.Git.URL == b.Spec.Source.Git.URL && equalGitRef(a.Spec.Source.Git.Ref, b.Spec.Source.Git.Ref)
	case a.Spec.Source.OCI != nil && b.Spec.Source.OCI != nil:
		return a.Spec.Source.OCI.URL == b.Spec.Source.OCI.URL
	}
	return false
}

func equalGitRef(a, b *infrav1.GitRef) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// cleanupRunner closes the connection and removes the runner Pod when
// configured to do so.
func (r *TerraformReconciler) cleanupRunner(ctx context.Context, tf *infrav1.Terraform, closeConn func() error) {
	log := ctrl.LoggerFrom(ctx)
	if closeConn != nil {
		if err := closeConn(); err != nil {
			log.Error(err, "unable to close runner connection")
		}
	}
	if os.Getenv("INSECURE_LOCAL_RUNNER") == "1" || !tf.Spec.Runner.GetAlwaysCleanup() {
		return
	}
	// Use a fresh context: the reconcile context may already be cancelled.
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pod := &corev1.Pod{}
	pod.Namespace, pod.Name = tf.Namespace, tf.RunnerPodName()
	if err := r.Delete(cleanupCtx, pod, client.GracePeriodSeconds(1), client.PropagationPolicy(metav1.DeletePropagationForeground)); client.IgnoreNotFound(err) != nil {
		log.Error(err, "unable to delete runner pod")
		return
	}
	err := wait.PollUntilContextTimeout(cleanupCtx, 2*time.Second, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		err := r.Get(ctx, client.ObjectKeyFromObject(pod), pod)
		return apierrors.IsNotFound(err), nil
	})
	if err != nil {
		log.Error(err, "timed out waiting for runner pod termination")
	}
}

func isQuotaError(err error) bool {
	return apierrors.IsForbidden(err) && strings.Contains(err.Error(), "exceeded quota")
}

var _ reconcile.Reconciler = &TerraformReconciler{}
