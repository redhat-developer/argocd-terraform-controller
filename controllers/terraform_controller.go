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
	"encoding/json"
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/argoproj/argo-cd/v2/common"
	"github.com/argoproj/argo-cd/v2/pkg/apis/application/v1alpha1"
	"github.com/argoproj/argo-cd/v2/reposerver/apiclient"
	"github.com/argoproj/argo-cd/v2/util/io"
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
	_ = r.Get(ctx, req.NamespacedName, terraform)

	l.Info("Reconciling", "name", terraform.Name, "url", terraform.Spec.Source.RepoURL)

	// default TLS settings
	// TODO: get the TLS config the same way the application controller does
	tlsConfig := apiclient.TLSConfiguration{
		DisableTLS:       false,
		StrictValidation: false,
	}

	repoClientSet := apiclient.NewRepoServerClientset(common.DefaultRepoServerAddr, 60, tlsConfig)

	// This creates a client to make gRPC calls to the repo server
	conn, repoClient, err := repoClientSet.NewRepoServerClient()
	if err != nil {
		l.Error(err, "error getting repo server client")
		return ctrl.Result{}, err
	}
	defer io.Close(conn)

	// TODO: call GenerateManifests the same way the application controller does
	manifestInfo, err := repoClient.GenerateManifest(context.Background(), &apiclient.ManifestRequest{
		Repo: &v1alpha1.Repository{
			Repo: terraform.Spec.Source.RepoURL,
		},
		NoCache: true,
		ApplicationSource: &v1alpha1.ApplicationSource{
			RepoURL:        terraform.Spec.Source.RepoURL,
			Path:           terraform.Spec.Source.Path,
			TargetRevision: terraform.Spec.Source.TargetRevision,
			Plugin: &v1alpha1.ApplicationSourcePlugin{
				Name: "argocd-terraform-generator",
			},
		},
		Plugins: []*v1alpha1.ConfigManagementPlugin{
			{
				Name: "argocd-terraform-generator",
				Generate: v1alpha1.Command{
					Command: []string{"bash", "-c"},
					Args:    []string{"argocd-terraform-generator"},
				},
			},
		},
	})
	if err != nil {
		l.Error(err, "error generating manifests")
		return ctrl.Result{}, err
	}

	terraformFiles := make([]argoprojiov1alpha1.TerraformFile, 0)
	for _, manifest := range manifestInfo.Manifests {
		obj, err := v1alpha1.UnmarshalToUnstructured(manifest)
		if err != nil {
			l.Error(err, "error unmarshaling to unstructured")
			return ctrl.Result{}, err
		}
		if obj.GetKind() == "TerraformWrapper" {
			var wrapper argoprojiov1alpha1.TerraformWrapper
			err := json.Unmarshal([]byte(manifest), &wrapper)
			if err != nil {
				l.Error(err, "Error unmarshaling manifest into wrapper")
			}
			terraformFiles = wrapper.List
		} else {
			l.Error(nil, "Only expected kubernetes objects of kind: TerraformWrapper")
			return ctrl.Result{}, errors.New("only expected kubernetes objects of kind: TerraformWrapper")
		}
	}

	l.Info(fmt.Sprintf("%+v", terraformFiles))

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *TerraformReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&argoprojiov1alpha1.Terraform{}).
		Complete(r)
}
