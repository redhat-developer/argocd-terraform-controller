#!/bin/bash
cd ..
kubectl create configmap -n $ARGOCD_APP_NAMESPACE $ARGOCD_APP_NAME-terraform --from-file=$ARGOCD_APP_SOURCE_PATH -o json --dry-run
echo "{\"apiVersion\": \"argoproj.io/v1alpha1\", \"kind\": \"Terraform\", \"metadata\": {\"name\": \"$ARGOCD_APP_NAME\", \"namespace\": \"$ARGOCD_APP_NAMESPACE\"}, \"spec\": {\"revision\": \"$ARGOCD_APP_REVISION\", \"completed\": false}}"
