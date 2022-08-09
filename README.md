# argocd-terraform-controller

Argo CD Terraform Controller

Start by creating an argocd cluster with the terraform-generate plugin
installed.

```
kubectl create ns argocd
kubectl apply -k terraform-generate/kustomize-core-install -n argocd
kubectl config set-context --current --namespace=argocd
```


Create the ArgoCD Terraform controller from the yaml:

```
kubectl apply -f controller.yaml
```


Now, create an ArgoCD app that uses the terraform-generate plugin, the repository should contain
the terraform manifests you want to track:

```
argocd app create terraform-test --repo <REPO URL> --path <REPO PATH> --dest-server https://kubernetes.default.svc --dest-namespace argocd --config-management-plugin argocd-terraform-generator
```


Sync the application to run the plugin and update the local terraform manifests

```
argocd app sync terraform-test
```


### Dev
To generate controller.yaml:
```
# install controller-gen and kustomize
make controller-gen
make kustomize 
mkdir bin
cp ~/path/to/controller-gen bin/
cp ~/path/to/kustomize bin/
IMG=<image> make deploy-file
```

To build controller and worker images and push them:
```
IMG=quay.io/jsawaya/argocd-tf-controller:latest make podman-build-no-test
IMG=quay.io/jsawaya/argocd-tf-controller:latest make podman-push
IMG=quay.io/jsawaya/terraform-controller-worker:latest make podman-build-worker-no-test
IMG=quay.io/jsawaya/terraform-controller-worker:latest make podman-push
```
