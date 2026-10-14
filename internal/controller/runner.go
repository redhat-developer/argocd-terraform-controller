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
	"maps"
	"net"
	"os"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	infrav1 "github.com/redhat-developer/argocd-terraform-controller/api/v1alpha1"
	"github.com/redhat-developer/argocd-terraform-controller/internal/mtls"
	"github.com/redhat-developer/argocd-terraform-controller/internal/runner"
)

const (
	runnerUID           = int64(65532)
	runnerContainerName = "tf-runner"
)

// runnerImage returns the image for tf's runner Pod.
func (r *TerraformReconciler) runnerImage(tf *infrav1.Terraform) string {
	if tf.Spec.Runner.Image != "" {
		return tf.Spec.Runner.Image
	}
	if r.RunnerImage != "" {
		return r.RunnerImage
	}
	if img := os.Getenv("RUNNER_POD_IMAGE"); img != "" {
		return img
	}
	return "quay.io/redhatworkshops/argocd-terraform-controller-runner:latest"
}

// runnerHostname returns the DNS name used to reach the runner Pod; Pod IP
// DNS names (a-b-c-d.<ns>.pod.<domain>) match the wildcard SAN of the
// per-namespace certificate.
func (r *TerraformReconciler) runnerHostname(tf *infrav1.Terraform, podIP string) string {
	if net.ParseIP(podIP) != nil {
		return fmt.Sprintf("%s.%s.pod.%s", strings.ReplaceAll(podIP, ".", "-"), tf.Namespace, r.ClusterDomain)
	}
	return podIP
}

// LookupOrCreateRunner ensures the TLS Secret and runner Pod exist and
// returns a connected client plus a close function.
func (r *TerraformReconciler) LookupOrCreateRunner(ctx context.Context, tf *infrav1.Terraform, revision string) (runner.RunnerClient, func() error, error) {
	log := ctrl.LoggerFrom(ctx)

	tlsSecret, err := r.CertRotator.EnsureNamespaceTLS(ctx, tf.Namespace)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to ensure runner TLS secret: %w", err)
	}

	var hostname string
	if os.Getenv("INSECURE_LOCAL_RUNNER") == "1" {
		hostname = "localhost"
	} else {
		podIP, err := r.reconcileRunnerPod(ctx, tf, tlsSecret.Name, revision)
		if err != nil {
			return nil, nil, err
		}
		hostname = r.runnerHostname(tf, podIP)
	}
	log.Info("connecting to runner", "hostname", hostname, "port", r.RunnerGRPCPort)

	creds, err := mtls.ClientCredentials(tlsSecret)
	if err != nil {
		return nil, nil, err
	}
	conn, err := r.dialRunner(hostname, creds)
	if err != nil {
		return nil, nil, err
	}
	return runner.NewRunnerClient(conn), conn.Close, nil
}

func (r *TerraformReconciler) dialRunner(hostname string, creds credentials.TransportCredentials) (*grpc.ClientConn, error) {
	const retryPolicy = `{
"methodConfig": [{
  "name": [{"service": "runner.Runner"}],
  "waitForReady": true,
  "retryPolicy": {
    "MaxAttempts": 4,
    "InitialBackoff": ".5s",
    "MaxBackoff": "5s",
    "BackoffMultiplier": 2.0,
    "RetryableStatusCodes": [ "UNAVAILABLE" ]
  }
}]}`
	maxMsg := r.RunnerGRPCMaxMessageSize * 1024 * 1024
	return grpc.NewClient(
		fmt.Sprintf("%s:%d", hostname, r.RunnerGRPCPort),
		grpc.WithTransportCredentials(creds),
		grpc.WithDefaultServiceConfig(retryPolicy),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(maxMsg), grpc.MaxCallSendMsgSize(maxMsg)),
	)
}

// runnerPodInstance derives a short instance label from the revision.
func runnerPodInstance(revision string) string {
	_, hash, ok := strings.Cut(revision, ":")
	if !ok || len(hash) < 8 {
		return "tf-runner"
	}
	return "tf-runner-" + hash[:8]
}

// runnerPod builds the desired runner Pod.
func (r *TerraformReconciler) runnerPod(tf *infrav1.Terraform, tlsSecretName, revision string) *corev1.Pod {
	tpl := tf.Spec.Runner.PodTemplate
	labels := map[string]string{
		"app.kubernetes.io/created-by": "argocd-terraform-controller",
		"app.kubernetes.io/name":       runnerContainerName,
		"app.kubernetes.io/instance":   runnerPodInstance(revision),
		infrav1.RunnerLabel:            tf.Namespace,
		infrav1.RunnerTLSSecretLabel:   tlsSecretName,
	}
	maps.Copy(labels, tpl.Metadata.Labels)

	env := map[string]corev1.EnvVar{
		"POD_NAME":      {Name: "POD_NAME", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"}}},
		"POD_NAMESPACE": {Name: "POD_NAMESPACE", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"}}},
	}
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY"} {
		if v := os.Getenv(name); v != "" {
			env[name] = corev1.EnvVar{Name: name, Value: v}
		}
	}
	for _, e := range tpl.Spec.Env {
		env[e.Name] = e
	}
	envVars := make([]corev1.EnvVar, 0, len(env))
	for _, k := range sortedKeys(env) {
		envVars = append(envVars, env[k])
	}

	volumes := append([]corev1.Volume{
		{Name: "temp", VolumeSource: corev1.VolumeSource{EmptyDir: new(corev1.EmptyDirVolumeSource)}},
		{Name: "home", VolumeSource: corev1.VolumeSource{EmptyDir: new(corev1.EmptyDirVolumeSource)}},
	}, tpl.Spec.Volumes...)
	mounts := append([]corev1.VolumeMount{
		{Name: "temp", MountPath: "/tmp"},
		{Name: "home", MountPath: runner.HomePath},
	}, tpl.Spec.VolumeMounts...)

	securityContext := &corev1.SecurityContext{
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		AllowPrivilegeEscalation: ptr.To(false),
		RunAsNonRoot:             ptr.To(true),
		RunAsUser:                ptr.To(runnerUID),
		ReadOnlyRootFilesystem:   ptr.To(true),
		SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
	if tpl.Spec.SecurityContext != nil {
		securityContext = tpl.Spec.SecurityContext
	}
	resources := corev1.ResourceRequirements{}
	if tpl.Spec.Resources != nil {
		resources = *tpl.Spec.Resources
	}

	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   tf.Namespace,
			Name:        tf.RunnerPodName(),
			Labels:      labels,
			Annotations: tpl.Metadata.Annotations,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: infrav1.GroupVersion.String(),
				Kind:       infrav1.TerraformKind,
				Name:       tf.Name,
				UID:        tf.UID,
				Controller: ptr.To(true),
			}},
		},
		Spec: corev1.PodSpec{
			TerminationGracePeriodSeconds: tf.Spec.Runner.TerminationGracePeriodSeconds,
			RestartPolicy:                 corev1.RestartPolicyNever,
			ServiceAccountName:            tf.Spec.Runner.GetServiceAccountName(),
			InitContainers:                tpl.Spec.InitContainers,
			Containers: []corev1.Container{{
				Name:  runnerContainerName,
				Image: r.runnerImage(tf),
				Args: []string{
					"--grpc-port", fmt.Sprintf("%d", r.RunnerGRPCPort),
					"--tls-secret-name", tlsSecretName,
					"--grpc-max-message-size", fmt.Sprintf("%d", r.RunnerGRPCMaxMessageSize),
				},
				ImagePullPolicy: corev1.PullIfNotPresent,
				Ports:           []corev1.ContainerPort{{Name: "grpc", ContainerPort: int32(r.RunnerGRPCPort)}},
				Env:             envVars,
				EnvFrom:         tpl.Spec.EnvFrom,
				SecurityContext: securityContext,
				VolumeMounts:    mounts,
				Resources:       resources,
			}},
			Volumes:           volumes,
			NodeSelector:      tpl.Spec.NodeSelector,
			Affinity:          tpl.Spec.Affinity,
			Tolerations:       tpl.Spec.Tolerations,
			HostAliases:       tpl.Spec.HostAliases,
			PriorityClassName: tpl.Spec.PriorityClassName,
		},
	}
}

func sortedKeys(m map[string]corev1.EnvVar) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// insertion sort keeps this dependency-free for a handful of entries
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

// reconcileRunnerPod makes sure a running runner Pod using tlsSecretName
// exists and returns its IP.
func (r *TerraformReconciler) reconcileRunnerPod(ctx context.Context, tf *infrav1.Terraform, tlsSecretName, revision string) (string, error) {
	log := ctrl.LoggerFrom(ctx)
	desired := r.runnerPod(tf, tlsSecretName, revision)
	key := client.ObjectKeyFromObject(desired)
	timeout := r.RunnerCreationTimeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}

	waitGone := func() error {
		return wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
			err := r.Get(ctx, key, &corev1.Pod{})
			return apierrors.IsNotFound(err), nil
		})
	}
	deleteAndRecreate := func(pod *corev1.Pod, grace int64, why string) error {
		log.Info("replacing runner pod", "reason", why)
		if err := r.Delete(ctx, pod, client.GracePeriodSeconds(grace), client.PropagationPolicy(metav1.DeletePropagationForeground)); client.IgnoreNotFound(err) != nil {
			return err
		}
		if err := waitGone(); err != nil {
			return fmt.Errorf("waiting for old runner pod termination: %w", err)
		}
		return r.Create(ctx, desired)
	}

	existing := &corev1.Pod{}
	err := r.Get(ctx, key, existing)
	grace := int64(30)
	if tf.Spec.Runner.TerminationGracePeriodSeconds != nil {
		grace = *tf.Spec.Runner.TerminationGracePeriodSeconds
	}
	switch {
	case apierrors.IsNotFound(err):
		log.Info("creating runner pod", "name", key.Name)
		if err := r.Create(ctx, desired); err != nil {
			return "", err
		}
	case err != nil:
		return "", fmt.Errorf("unable to get runner pod: %w", err)
	case existing.Labels[infrav1.RunnerTLSSecretLabel] == "":
		if err := deleteAndRecreate(existing, 1, "pod was not created by the controller"); err != nil {
			return "", err
		}
	case existing.Labels[infrav1.RunnerTLSSecretLabel] != tlsSecretName:
		if err := deleteAndRecreate(existing, grace, "TLS secret rotated"); err != nil {
			return "", err
		}
	case existing.DeletionTimestamp != nil:
		if err := waitGone(); err != nil {
			return "", fmt.Errorf("waiting for terminating runner pod: %w", err)
		}
		if err := r.Create(ctx, desired); err != nil {
			return "", err
		}
	case existing.Status.Phase == corev1.PodFailed || existing.Status.Phase == corev1.PodSucceeded:
		if err := deleteAndRecreate(existing, 1, "pod is in phase "+string(existing.Status.Phase)); err != nil {
			return "", err
		}
	}

	// Wait for an IP and the container to be running.
	var podIP string
	err = wait.PollUntilContextTimeout(ctx, time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		pod := &corev1.Pod{}
		if err := r.Get(ctx, key, pod); err != nil {
			return false, client.IgnoreNotFound(err)
		}
		if pod.Status.Phase == corev1.PodFailed {
			return false, fmt.Errorf("runner pod failed: %s", pod.Status.Message)
		}
		if pod.Status.PodIP == "" {
			return false, nil
		}
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.Name == runnerContainerName && cs.State.Running != nil {
				podIP = pod.Status.PodIP
				return true, nil
			}
		}
		return false, nil
	})
	if err != nil {
		pod := &corev1.Pod{}
		pod.Namespace, pod.Name = key.Namespace, key.Name
		_ = r.Delete(ctx, pod, client.GracePeriodSeconds(1))
		return "", fmt.Errorf("runner pod did not become ready: %w", err)
	}
	return podIP, nil
}
