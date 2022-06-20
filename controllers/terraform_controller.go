/*
Copyright 2022.

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

package controllers

import (
	"context"
	"fmt"
	"os"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	argoprojiov1alpha1 "github.com/sabre1041/argocd-terraform-controller/api/v1alpha1"
)

// TerraformReconciler reconciles a Terraform object
type TerraformReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=argoproj.io,resources=terraforms,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=argoproj.io,resources=terraforms/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=argoproj.io,resources=terraforms/finalizers,verbs=update
//+kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=rolebindings,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Terraform object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.11.0/pkg/reconcile
func (r *TerraformReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	l := log.FromContext(ctx)

	terraform := &argoprojiov1alpha1.Terraform{}

	l.Info(fmt.Sprintf("Req Namespaced names: %+v", req.NamespacedName))

	err := r.Get(ctx, req.NamespacedName, terraform)
	if err != nil {
		l.Error(err, "Error getting Terraform resource to reconcile")
		return ctrl.Result{}, err
	}

	l.Info(fmt.Sprintf("%+v", terraform))

	image := "quay.io/jsawaya/terraform-controller-worker:latest"
	workerImageEnvVar, workerImageEnvVarExists := os.LookupEnv("WORKER_IMG")
	if workerImageEnvVarExists {
		image = workerImageEnvVar
	}

	role := &rbacv1.Role{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Role",
			APIVersion: "rbac.authorization.k8s.io/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "argocd-terraform-worker-role",
			Namespace: req.Namespace,
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{
					"argoproj.io",
				},
				Resources: []string{
					"terraforms",
				},
				Verbs: []string{
					"get",
					"patch",
				},
			},
			{
				APIGroups: []string{
					"",
				},
				Resources: []string{
					"pods",
				},
				Verbs: []string{
					"get",
					"create",
					"list",
					"patch",
					"update",
					"delete",
					"watch",
				},
			},
			{
				APIGroups: []string{
					"",
				},
				Resources: []string{
					"secrets",
				},
				Verbs: []string{
					"get",
					"create",
					"list",
					"update",
				},
			},
			{
				APIGroups: []string{
					"coordination.k8s.io",
				},
				Resources: []string{
					"leases",
				},
				Verbs: []string{
					"get",
					"create",
					"update",
				},
			},
		},
	}

	err = r.Get(ctx, types.NamespacedName{
		Name:      "argocd-terraform-worker-role",
		Namespace: req.Namespace,
	}, &rbacv1.Role{})
	if err != nil {
		if client.IgnoreNotFound(err) != nil {
			l.Error(err, "Error getting Terraform resource to reconcile")
			return ctrl.Result{}, err
		} else {
			err := r.Create(ctx, role)
			if err != nil {
				l.Error(err, "Error creating role")
				return ctrl.Result{}, err
			}
		}
	}

	serviceAccount := &corev1.ServiceAccount{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ServiceAccount",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "argocd-terraform-worker",
			Namespace: req.Namespace,
		},
	}
	err = r.Get(ctx, types.NamespacedName{
		Name:      "argocd-terraform-worker",
		Namespace: req.Namespace,
	}, &corev1.ServiceAccount{})
	if err != nil {
		if client.IgnoreNotFound(err) != nil {
			l.Error(err, "Error getting Terraform resource to reconcile")
			return ctrl.Result{}, err
		} else {
			err := r.Create(ctx, serviceAccount)
			if err != nil {
				l.Error(err, "Error creating serviceAccount")
				return ctrl.Result{}, err
			}
		}
	}

	roleBinding := &rbacv1.RoleBinding{
		TypeMeta: metav1.TypeMeta{
			Kind:       "RoleBinding",
			APIVersion: "rbac.authorization.k8s.io/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "argocd-terraform-worker-rolebinding",
			Namespace: req.Namespace,
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     "argocd-terraform-worker-role",
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      "argocd-terraform-worker",
				Namespace: req.Namespace,
			},
		},
	}

	err = r.Get(ctx, types.NamespacedName{
		Name:      "argocd-terraform-worker-rolebinding",
		Namespace: req.Namespace,
	}, &rbacv1.RoleBinding{})
	if err != nil {
		if client.IgnoreNotFound(err) != nil {
			l.Error(err, "Error getting Terraform resource to reconcile")
			return ctrl.Result{}, err
		} else {
			err := r.Create(ctx, roleBinding)
			if err != nil {
				l.Error(err, "Error creating role binding")
				return ctrl.Result{}, err
			}
		}
	}

	optional := true
	pod := &corev1.Pod{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Pod",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "worker-pod-" + req.Name,
			Namespace: req.Namespace,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:    "terraform-controller-worker-" + req.Name,
					Image:   image,
					Command: []string{"/usr/local/bin/terraform-controller-worker"},
					Args:    []string{req.Namespace, req.Name},
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      "files",
							MountPath: "/opt/manifests/config",
						},
					},
					EnvFrom: []corev1.EnvFromSource{
						{
							SecretRef: &corev1.SecretEnvSource{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: "argocd-terraform-credentials",
								},
								Optional: &optional,
							},
						},
					},
				},
			},
			ServiceAccountName: "argocd-terraform-worker",
			RestartPolicy:      corev1.RestartPolicyOnFailure,
			Volumes: []corev1.Volume{
				{
					Name: "files",
					VolumeSource: corev1.VolumeSource{
						ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: req.Name + "-terraform",
							},
						},
					},
				},
			},
		},
	}

	if terraform.Spec.Completed == true {
		err = r.Delete(ctx, pod)
		if err != nil {
			l.Error(err, "Error deleting pod")
			return ctrl.Result{}, err
		}
		l.Info("Deleted")
	} else {
		err = r.Create(ctx, pod)
		if err != nil {
			l.Error(err, "Error creating pod")
			return ctrl.Result{}, err
		}
		l.Info("Created")
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *TerraformReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&argoprojiov1alpha1.Terraform{}).
		Complete(r)
}
