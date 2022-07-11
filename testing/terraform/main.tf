terraform {
  required_providers {
    docker = {
      source = "kreuzwerker/docker"
      version = " ~> 2.13.0"
    }
  }
}

provider "docker" {}

resource "docker_image" "nginx" {
  name = "nginx:latest"
  keep_locally = false
}

resource "docker_container" "nginx1" {
  image = docker_image.nginx.latest
  name = "con1"
  ports {
    internal = 80
    external = 8000
  }
}

resource "docker_container" "nginx2" {
  image = docker_image.nginx.latest
  name = "con2"
  ports {
    internal = 80
    external = 8001
  }
}
