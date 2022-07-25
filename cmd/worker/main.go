package main

import (
	"context"
	"fmt"
	"os"

	"github.com/hashicorp/terraform-exec/tfexec"
	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"

	"github.com/otiai10/copy"
	argoprojiov1alpha1 "github.com/sabre1041/argocd-terraform-controller/api/v1alpha1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
)

var (
	scheme = runtime.NewScheme()
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(argoprojiov1alpha1.AddToScheme(scheme))
}

type TerraformFile struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

var RootCmd = &cobra.Command{
	Use:   "work",
	Short: "Command to run the terraform-controller's worker",
	Args:  cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		namespace := args[0]
		name := args[1]

		cl, err := client.New(config.GetConfigOrDie(), client.Options{
			Scheme: scheme,
		})
		if err != nil {
			fmt.Println("failed to create client")
			os.Exit(1)
		}

		ctx := context.Background()

		configMapDir := "/opt/manifests/config"
		workingDir := "/opt/manifests/readable"

		copy.Copy(configMapDir, workingDir)

		tf, err := tfexec.NewTerraform(workingDir, "/usr/local/bin/terraform")
		if err != nil {
			klog.Errorf("error running NewTerraform: %s", err)
		}

		klog.Infof("NewTerraform Complete")

		backendConfig := fmt.Sprintf(`
terraform {
	backend "kubernetes" {
		secret_suffix     = "%s-tf-controller"
		in_cluster_config = true
		namespace         = "%s"
	}
}`, name, namespace)

		os.WriteFile("/opt/manifests/readable/backend.tf", []byte(backendConfig), 0644)

		err = tf.Init(ctx, tfexec.Upgrade(true))
		if err != nil {
			klog.Errorf("error running Init: %s", err)
		}

		_, err = tf.Plan(ctx)
		if err != nil {
			klog.Errorf("error running Plan: %s", err)
		}

		err = tf.Apply(ctx)
		if err != nil {
			klog.Errorf("error running Apply: %s", err)
		}

		terra := argoprojiov1alpha1.Terraform{}

		err = cl.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &terra)
		if err != nil {
			klog.Errorf("Error getting %s: %v", name, err)
		}

		newTerra := terra.DeepCopy()

		newTerra.Spec.Completed = true

		err = cl.Patch(ctx, newTerra, client.MergeFrom(terra.DeepCopy()))
		if err != nil {
			klog.Errorf("Error patching %s: %v", name, err)
		}
	},
}

func main() {
	RootCmd.Execute()
}
