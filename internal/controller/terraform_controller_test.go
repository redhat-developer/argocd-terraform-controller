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
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	infrav1 "github.com/redhat-developer/argocd-terraform-controller/api/v1alpha1"
	"github.com/redhat-developer/argocd-terraform-controller/internal/plan"
)

const helloTF = `
variable "greeting" {
  type    = string
  default = "hello"
}
resource "terraform_data" "hello" {
  input = var.greeting
}
output "hello_world" {
  value = terraform_data.hello.output
}
`

var _ = Describe("Terraform controller", func() {
	It("plans and applies automatically and writes outputs", func() {
		repo := newTestRepo(map[string]string{"main.tf": helloTF})
		tf := newTerraform("auto-apply", repo, func(tf *infrav1.Terraform) {
			tf.Spec.ApprovePlan = infrav1.ApprovePlanAutoValue
			tf.Spec.WriteOutputsToSecret = &infrav1.WriteOutputsToSecretSpec{Name: "auto-apply-outputs"}
		})
		Expect(k8sClient.Create(ctx, tf)).To(Succeed())
		key := client.ObjectKeyFromObject(tf)
		DeferCleanup(func() { deleteAndWait(getTF(key)) })

		By("becoming Ready")
		Eventually(readyStatus(key), timeout, interval).Should(Equal(metav1.ConditionTrue))
		got := getTF(key)
		Expect(got.Status.Plan.Pending).To(BeEmpty())
		Expect(got.Status.Plan.LastApplied).To(HavePrefix("plan-main-"))
		Expect(got.Status.LastAppliedRevision).To(HavePrefix("main@sha1:"))
		Expect(got.Status.LastAppliedRevision).To(Equal(got.Status.LastAttemptedRevision))
		Expect(got.Status.Binary).To(SatisfyAny(HavePrefix("tofu "), HavePrefix("terraform ")))
		Expect(got.Status.StateSecretName).To(BeEmpty(), "custom backend must not advertise a state secret")
		Expect(got.Status.AvailableOutputs).To(Equal([]string{"hello_world"}))
		Expect(got.Status.PlanConfigMapName).To(Equal("tfplan-default-auto-apply-readable"))
		Expect(got.GetCondition(infrav1.ConditionTypeApply).Status).To(Equal(metav1.ConditionTrue))
		Expect(got.Finalizers).To(ContainElement(infrav1.TerraformFinalizer))

		By("writing the outputs Secret owned by the Terraform object")
		outputs := &corev1.Secret{}
		Eventually(func() error {
			return k8sClient.Get(ctx, client.ObjectKey{Namespace: testNamespace, Name: "auto-apply-outputs"}, outputs)
		}, timeout, interval).Should(Succeed())
		Expect(string(outputs.Data["hello_world"])).To(Equal("hello"))
		Expect(outputs.OwnerReferences).To(HaveLen(1))
		Expect(outputs.OwnerReferences[0].Name).To(Equal("auto-apply"))

		By("storing the plan Secret and the human readable plan ConfigMap")
		planSecrets := &corev1.SecretList{}
		Expect(k8sClient.List(ctx, planSecrets, client.InNamespace(testNamespace), client.MatchingLabels(plan.Labels("auto-apply", "default", "")))).To(Succeed())
		Expect(planSecrets.Items).To(HaveLen(1))
		Expect(planSecrets.Items[0].Annotations[plan.SavedAnnotation]).To(Equal(got.Status.Plan.LastApplied))
		cm := &corev1.ConfigMap{}
		Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: testNamespace, Name: got.Status.PlanConfigMapName}, cm)).To(Succeed())
		Expect(cm.Data[plan.DataKey]).To(ContainSubstring("terraform_data.hello"))
	})

	It("waits for manual approval and applies the approved plan", func() {
		repo := newTestRepo(map[string]string{"main.tf": helloTF})
		tf := newTerraform("manual-approve", repo, nil)
		Expect(k8sClient.Create(ctx, tf)).To(Succeed())
		key := client.ObjectKeyFromObject(tf)
		DeferCleanup(func() { deleteAndWait(getTF(key)) })

		By("producing a pending plan and an approval prompt")
		Eventually(func() string { return getTF(key).Status.Plan.Pending }, timeout, interval).Should(HavePrefix("plan-main-"))
		pending := getTF(key).Status.Plan.Pending
		ready := readyCondition(key)()
		Expect(ready.Status).To(Equal(metav1.ConditionUnknown))
		Expect(ready.Reason).To(Equal(infrav1.PlannedWithChangesReason))
		Expect(ready.Message).To(ContainSubstring(fmt.Sprintf("set approvePlan: %q", pending)))
		Expect(getTF(key).GetCondition(infrav1.ConditionTypeApply)).To(BeNil(), "nothing applied before approval")

		By("rejecting a wrong approval")
		Consistently(func() string { return getTF(key).Status.Plan.LastApplied }, "3s", interval).Should(BeEmpty())

		By("re-planning when a reconcile is requested while the plan is pending")
		before := getTF(key)
		firstPlanAt := before.Status.LastPlanAt
		before.Annotations = map[string]string{ReconcileRequestAnnotation: "2026-01-01T00:00:00Z"}
		Expect(k8sClient.Update(ctx, before)).To(Succeed())
		Eventually(func() string { return getTF(key).Status.LastHandledReconcileAt }, timeout, interval).Should(Equal("2026-01-01T00:00:00Z"))
		Eventually(func() bool {
			got := getTF(key)
			return got.Status.Plan.Pending == pending && got.Status.LastPlanAt != nil && got.Status.LastPlanAt.After(firstPlanAt.Time)
		}, timeout, interval).Should(BeTrue(), "a fresh plan with the same id should be generated")
		Expect(getTF(key).Status.Plan.LastApplied).To(BeEmpty())

		By("applying once the plan ID is approved")
		got := getTF(key)
		got.Spec.ApprovePlan = pending
		Expect(k8sClient.Update(ctx, got)).To(Succeed())
		Eventually(func() string { return getTF(key).Status.Plan.LastApplied }, timeout, interval).Should(Equal(pending))
		Eventually(readyStatus(key), timeout, interval).Should(Equal(metav1.ConditionTrue))
		Expect(getTF(key).Status.Plan.Pending).To(BeEmpty())
	})

	It("re-plans when the source revision changes", func() {
		repo := newTestRepo(map[string]string{"main.tf": helloTF})
		tf := newTerraform("source-change", repo, func(tf *infrav1.Terraform) {
			tf.Spec.ApprovePlan = infrav1.ApprovePlanAutoValue
			tf.Spec.Source.Interval = &metav1.Duration{Duration: 2e9}
			tf.Spec.WriteOutputsToSecret = &infrav1.WriteOutputsToSecretSpec{Name: "source-change-outputs"}
		})
		Expect(k8sClient.Create(ctx, tf)).To(Succeed())
		key := client.ObjectKeyFromObject(tf)
		DeferCleanup(func() { deleteAndWait(getTF(key)) })

		Eventually(readyStatus(key), timeout, interval).Should(Equal(metav1.ConditionTrue))
		first := getTF(key).Status.LastAppliedRevision

		By("committing a change to the source")
		sha := repo.commit(map[string]string{"main.tf": strings.Replace(helloTF, `default = "hello"`, `default = "bonjour"`, 1)}, "change greeting")

		Eventually(func() string { return getTF(key).Status.LastAppliedRevision }, timeout, interval).Should(Equal("main@sha1:" + sha))
		Expect(getTF(key).Status.LastAppliedRevision).NotTo(Equal(first))
		Eventually(func() string {
			s := &corev1.Secret{}
			if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: testNamespace, Name: "source-change-outputs"}, s); err != nil {
				return ""
			}
			return string(s.Data["hello_world"])
		}, timeout, interval).Should(Equal("bonjour"))
	})

	It("passes vars and varsFrom to the configuration", func() {
		repo := newTestRepo(map[string]string{"main.tf": `
variable "greeting" { type = string }
variable "who" { type = string }
output "message" { value = "${var.greeting}, ${var.who}" }
`})
		cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "vars-cm", Namespace: testNamespace}, Data: map[string]string{"who": "world"}}
		Expect(k8sClient.Create(ctx, cm)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, cm) })

		tf := newTerraform("with-vars", repo, func(tf *infrav1.Terraform) {
			tf.Spec.ApprovePlan = infrav1.ApprovePlanAutoValue
			tf.Spec.Vars = []infrav1.Variable{{Name: "greeting", Value: jsonValue(`"hi"`)}}
			tf.Spec.VarsFrom = []infrav1.VarsReference{{Kind: "ConfigMap", Name: "vars-cm"}}
			tf.Spec.WriteOutputsToSecret = &infrav1.WriteOutputsToSecretSpec{Name: "with-vars-outputs", Outputs: []string{"message:msg"}}
		})
		Expect(k8sClient.Create(ctx, tf)).To(Succeed())
		key := client.ObjectKeyFromObject(tf)
		DeferCleanup(func() { deleteAndWait(getTF(key)) })

		Eventually(readyStatus(key), timeout, interval).Should(Equal(metav1.ConditionTrue))
		s := &corev1.Secret{}
		Eventually(func() error {
			return k8sClient.Get(ctx, client.ObjectKey{Namespace: testNamespace, Name: "with-vars-outputs"}, s)
		}, timeout, interval).Should(Succeed())
		Expect(s.Data).To(HaveKeyWithValue("msg", []byte("hi, world")))
		Expect(s.Data).NotTo(HaveKey("message"))
	})

	It("destroys resources and cleans up on deletion", func() {
		repo := newTestRepo(map[string]string{"main.tf": helloTF})
		tf := newTerraform("destroy-on-delete", repo, func(tf *infrav1.Terraform) {
			tf.Spec.ApprovePlan = infrav1.ApprovePlanAutoValue
			tf.Spec.DestroyResourcesOnDeletion = true
			tf.Spec.WriteOutputsToSecret = &infrav1.WriteOutputsToSecretSpec{Name: "destroy-outputs"}
		})
		Expect(k8sClient.Create(ctx, tf)).To(Succeed())
		key := client.ObjectKeyFromObject(tf)
		Eventually(readyStatus(key), timeout, interval).Should(Equal(metav1.ConditionTrue))

		statePath := strings.TrimSuffix(strings.TrimPrefix(tf.Spec.BackendConfig.CustomConfiguration, "backend \"local\" {\n    path = \""), "\"\n  }")
		Expect(statePath).To(BeAnExistingFile())

		By("deleting the object")
		deleteAndWait(getTF(key))

		By("removing plan and output Secrets")
		Eventually(func() int {
			list := &corev1.SecretList{}
			_ = k8sClient.List(ctx, list, client.InNamespace(testNamespace), client.MatchingLabels(plan.Labels("destroy-on-delete", "default", "")))
			return len(list.Items)
		}, timeout, interval).Should(BeZero())
		Eventually(func() bool {
			err := k8sClient.Get(ctx, client.ObjectKey{Namespace: testNamespace, Name: "destroy-outputs"}, &corev1.Secret{})
			return err != nil
		}, timeout, interval).Should(BeTrue())

		By("having emptied the state")
		data, err := readFile(statePath)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).NotTo(ContainSubstring(`"type": "terraform_data"`))
	})

	It("respects suspend and planOnly", func() {
		repo := newTestRepo(map[string]string{"main.tf": helloTF})
		tf := newTerraform("suspended", repo, func(tf *infrav1.Terraform) {
			tf.Spec.Suspend = true
		})
		Expect(k8sClient.Create(ctx, tf)).To(Succeed())
		key := client.ObjectKeyFromObject(tf)
		DeferCleanup(func() { deleteAndWait(getTF(key)) })

		Eventually(func() string {
			if c := readyCondition(key)(); c != nil {
				return c.Reason
			}
			return ""
		}, timeout, interval).Should(Equal(infrav1.SuspendedReason))
		Consistently(func() string { return getTF(key).Status.Plan.Pending }, "3s", interval).Should(BeEmpty())

		By("planning but never applying once resumed in planOnly mode")
		got := getTF(key)
		got.Spec.Suspend = false
		got.Spec.PlanOnly = true
		got.Spec.ApprovePlan = infrav1.ApprovePlanAutoValue
		Expect(k8sClient.Update(ctx, got)).To(Succeed())
		Eventually(func() string { return getTF(key).Status.Plan.Pending }, timeout, interval).Should(HavePrefix("plan-main-"))
		Consistently(func() string { return getTF(key).Status.Plan.LastApplied }, "3s", interval).Should(BeEmpty())
		Expect(readyCondition(key)().Message).To(ContainSubstring("plan only mode"))
	})

	It("fails clearly when the source path does not exist", func() {
		repo := newTestRepo(map[string]string{"main.tf": helloTF})
		tf := newTerraform("bad-path", repo, func(tf *infrav1.Terraform) {
			tf.Spec.Path = "./does-not-exist"
			tf.Spec.ApprovePlan = infrav1.ApprovePlanAutoValue
		})
		Expect(k8sClient.Create(ctx, tf)).To(Succeed())
		key := client.ObjectKeyFromObject(tf)
		DeferCleanup(func() { deleteAndWait(getTF(key)) })

		Eventually(func() string {
			if c := readyCondition(key)(); c != nil && c.Status == metav1.ConditionFalse {
				return c.Reason
			}
			return ""
		}, timeout, interval).Should(Equal(infrav1.SourceFetchFailedReason))
		Expect(readyCondition(key)().Message).To(ContainSubstring("does-not-exist"))
		Expect(getTF(key).Status.ReconciliationFailures).To(BeNumerically(">=", 1))
	})

	It("blocks deletion of a dependency while a dependant exists", func() {
		repo := newTestRepo(map[string]string{"main.tf": helloTF})
		base := newTerraform("dep-base", repo, func(tf *infrav1.Terraform) {
			tf.Spec.ApprovePlan = infrav1.ApprovePlanAutoValue
		})
		Expect(k8sClient.Create(ctx, base)).To(Succeed())
		baseKey := client.ObjectKeyFromObject(base)
		Eventually(readyStatus(baseKey), timeout, interval).Should(Equal(metav1.ConditionTrue))

		child := newTerraform("dep-child", repo, func(tf *infrav1.Terraform) {
			tf.Spec.ApprovePlan = infrav1.ApprovePlanAutoValue
			tf.Spec.DependsOn = []infrav1.NamespacedObjectReference{{Name: "dep-base"}}
		})
		Expect(k8sClient.Create(ctx, child)).To(Succeed())
		childKey := client.ObjectKeyFromObject(child)
		Eventually(readyStatus(childKey), timeout, interval).Should(Equal(metav1.ConditionTrue))
		Expect(getTF(baseKey).Finalizers).To(ContainElement(infrav1.DependencyFinalizerPrefix + "dep-child"))

		By("refusing to finalize the dependency")
		Expect(k8sClient.Delete(ctx, getTF(baseKey))).To(Succeed())
		Eventually(func() string {
			if c := readyCondition(baseKey)(); c != nil {
				return c.Reason
			}
			return ""
		}, timeout, interval).Should(Equal(infrav1.DeletionBlockedByDependants))

		By("finalizing once the dependant is gone")
		deleteAndWait(getTF(childKey))
		Eventually(func() bool {
			err := k8sClient.Get(ctx, baseKey, &infrav1.Terraform{})
			return err != nil
		}, timeout, interval).Should(BeTrue())
	})
})
