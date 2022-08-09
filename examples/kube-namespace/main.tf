provider "kubernetes" {}

resource "kubernetes_namespace" "example" {
  metadata {
    name = "my-first-namespace-1"
  }
}