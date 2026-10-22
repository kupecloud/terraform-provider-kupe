# Every pod of the prod cluster can pull from the registry. Nothing is needed
# in the pods: no imagePullSecrets.
resource "kupe_registry_binding" "prod" {
  cluster = kupe_cluster.prod.name
}

# The ci cluster can pull too, and its build namespace gets a push credential
# Secret, kupe-registry-push, for in-cluster image builds.
resource "kupe_registry_binding" "ci" {
  cluster         = kupe_cluster.ci.name
  push_namespaces = ["builds"]
}
