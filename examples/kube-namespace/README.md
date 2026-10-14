# kube-namespace example

Creates a Namespace and a ConfigMap with the Kubernetes provider using the
runner's ServiceAccount. The default `tf-runner` ServiceAccount can only
manage Secrets, ConfigMaps and Leases in its own namespace, so grant it the
extra permissions this example needs:

```sh
kubectl create clusterrolebinding tf-runner-namespaces \
  --clusterrole=cluster-admin --serviceaccount=<namespace>:tf-runner
```

(Use a narrower ClusterRole in real deployments.)
