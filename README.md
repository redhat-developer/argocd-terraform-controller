# argocd-terraform-controller

Argo CD Terraform Controller

To generate controller.yaml:
```
# install controller-gen and kustomize
make controller-gen
make kustomize 
mkdir bin
cp ~/path/to/controller-gen bin/
cp ~/path/to/kustomize bin/
make deploy-file
```

To run:

```
kubectl create ns argocd
kubectl apply -k terraform-generate/kustomize-core-install -n argocd
kubectl apply -f controller.yaml
kubectl config set-context --current --namespace=argocd
```

ArgoCD app commands:
```
argocd app create terraform-test --repo <REPO URL> --path <REPO PATH> --dest-server https://kubernetes.default.svc --dest-namespace argocd --config-management-plugin argocd-terraform-generator
argocd app sync terraform-test
```