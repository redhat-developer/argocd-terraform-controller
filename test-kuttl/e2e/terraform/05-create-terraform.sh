#! /bin/bash
set -e -o pipefail

kubectl apply -n argocd -f - <<EOF
---
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: terraform-test
spec:
  destination:
    namespace: argocd
    server: https://kubernetes.default.svc
  syncPolicy:
    automated: {}
  project: default
  source:
    path: config/samples
    plugin:
      name: argocd-terraform-generator
    repoURL: https://github.com/sabre1041/argocd-terraform-controller.git
EOF
