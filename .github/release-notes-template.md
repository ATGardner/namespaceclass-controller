## Install

**Prerequisite:** the admission webhooks need TLS certs, provisioned via
[cert-manager](https://cert-manager.io). Install it first if the cluster
doesn't already have it:

```sh
helm install cert-manager oci://quay.io/jetstack/charts/cert-manager \
  --namespace cert-manager --create-namespace \
  --version v1.21.2 --set crds.enabled=true
```

Pick one of the following to install namespaceclass-controller itself:

**Helm:**

```sh
helm install namespaceclass-controller \
  oci://ghcr.io/atgardner/charts/namespaceclass-controller \
  --version __VERSION__ \
  --namespace namespaceclass-controller-system \
  --create-namespace
```

**Plain YAML bundle (no Helm):**

```sh
kubectl apply -f https://raw.githubusercontent.com/atgardner/namespaceclass-controller/__TAG__/dist/install.yaml
```

**Kustomize:** build directly off this release's `config/` tree, overriding
the image to the one actually published for this tag:

```sh
mkdir -p namespaceclass-controller-install && cd namespaceclass-controller-install
cat <<EOF > kustomization.yaml
resources:
  - github.com/atgardner/namespaceclass-controller/config/default?ref=__TAG__
images:
  - name: controller
    newName: ghcr.io/atgardner/namespaceclass-controller
    newTag: __VERSION__
EOF
kubectl apply -k .
```

Then apply a `NamespaceClass` and label a namespace with
`namespaceclass.akuity.io/name=<class-name>` — see `config/samples/`.

## Published artifacts

- Image: __IMAGE_URL__
- Helm chart: __CHART_URL__
