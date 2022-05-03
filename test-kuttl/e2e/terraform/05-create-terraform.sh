#! /bin/bash
set -e -o pipefail

kubectl apply -n default -f - <<EOF
---
apiVersion: argoproj.io/v1alpha1
kind: Terraform
metadata:
  name: terraform-sample
spec:
  # TODO(user): Add fields here
EOF
