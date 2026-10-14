# Minimal example: manage a Namespace and a ConfigMap with the Kubernetes
# provider, authenticated with the runner's ServiceAccount token.
terraform {
  required_providers {
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = ">= 2.0"
    }
  }
}

provider "kubernetes" {
  # In-cluster configuration via the runner ServiceAccount.
  host                   = "https://kubernetes.default.svc"
  cluster_ca_certificate = file("/var/run/secrets/kubernetes.io/serviceaccount/ca.crt")
  token                  = file("/var/run/secrets/kubernetes.io/serviceaccount/token")
}

variable "namespace" {
  type    = string
  default = "terraform-demo"
}

resource "kubernetes_namespace" "demo" {
  metadata {
    name = var.namespace
  }
}

resource "kubernetes_config_map" "demo" {
  metadata {
    name      = "managed-by-terraform"
    namespace = kubernetes_namespace.demo.metadata[0].name
  }
  data = {
    message = "hello from the Argo CD Terraform Controller"
  }
}

output "namespace" {
  value = kubernetes_namespace.demo.metadata[0].name
}
