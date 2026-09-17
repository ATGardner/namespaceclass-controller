## Install

```sh
helm install namespaceclass-controller \
  oci://ghcr.io/atgardner/charts/namespaceclass-controller \
  --version __VERSION__ \
  --namespace namespaceclass-controller-system \
  --create-namespace
```

Then apply a `NamespaceClass` and label a namespace with
`namespaceclass.akuity.io/name=<class-name>` — see `config/samples/`.

## Published artifacts

- Image: __IMAGE_URL__
- Helm chart: __CHART_URL__
