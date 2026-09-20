# namespaceclass-controller
// TODO(user): Add simple overview of use/purpose

## Description
// TODO(user): An in-depth paragraph about your project and overview of use

## Drift Correction

Resources are reconciled with Kubernetes [Server-Side Apply](https://kubernetes.io/docs/reference/using-api/server-side-apply/)
(`client.Apply` with `ForceOwnership`, this controller as the field manager).
That means drift correction is scoped to the fields a `NamespaceClass`
actually templates: if a `ConfigMap` key or label the class defines gets
hand-edited, the next reconcile reverts it. Fields the class never
mentioned — an extra label, an extra `data` key someone else added — are
left alone, because this controller never claimed ownership of them in the
first place.

This is a deliberate choice, not a gap: it's the same field-manager model
Kubernetes itself uses so multiple writers (an HPA and a Deployment
controller both touching one Deployment, a mutating webhook injecting
compliance labels, an auto-populated `ServiceAccount` field) can coexist on
one object without fighting over fields they don't own. A `NamespaceClass`
resource is expected to share space with other legitimate cluster tooling
under this model, rather than assume it's the object's only writer.

**Future work:** some use cases (e.g. a security baseline that must never
drift, even by an added field) want the opposite guarantee — full,
exclusive ownership, where any change or addition not in the template gets
stripped. That would mean a `Get` + full-object `Update` instead of a
`Patch`/Apply, and is a real, valid alternative for those cases — but it
also means the controller will fight any other legitimate writer touching
the same resource. A natural extension would be a per-`NamespaceClass` field
(e.g. `spec.strict: true`) letting the class author pick coexistence
(default) vs. exclusive ownership per class, rather than picking one
behavior globally. Not implemented here.

## Quick Install

The controller image and Helm chart are published to GHCR on every tagged
release (see `.github/workflows/release.yml`). To run it in any cluster,
no local build required:

**Prerequisite:** the chart's validating and mutating webhooks need TLS
certs, provisioned via [cert-manager](https://cert-manager.io). Install it
first if the cluster doesn't already have it:

```sh
helm install cert-manager oci://quay.io/jetstack/charts/cert-manager \
  --namespace cert-manager --create-namespace \
  --version v1.21.2 --set crds.enabled=true
```

```sh
helm install namespaceclass-controller \
  oci://ghcr.io/atgardner/charts/namespaceclass-controller \
  --version <released-version> \
  --namespace namespaceclass-controller-system \
  --create-namespace
```

Then apply a `NamespaceClass` and label a namespace with
`namespaceclass.akuity.io/name=<class-name>` — see `config/samples/`.

To skip the cert-manager dependency entirely, pass
`--set certManager.enabled=false,webhook.enabled=false` — this disables both
webhooks (admission-time validation and real-time drift enforcement), not
just their certs; the reconciler's own reconcile-loop checks still cover the
same ground, just without the immediate feedback.

## Getting Started

### Prerequisites
- go version v1.24.6+
- docker version 17.03+.
- kubectl version v1.11.3+.
- Access to a Kubernetes v1.11.3+ cluster.
- [cert-manager](https://cert-manager.io) installed in the cluster (required
  for webhook TLS certs — see Quick Install above).

### To Deploy on the cluster
**Build and push your image to the location specified by `IMG`:**

```sh
make docker-build docker-push IMG=<some-registry>/namespaceclass-controller:tag
```

**NOTE:** This image ought to be published in the personal registry you specified.
And it is required to have access to pull the image from the working environment.
Make sure you have the proper permission to the registry if the above commands don’t work.

**Install the CRDs into the cluster:**

```sh
make install
```

**Deploy the Manager to the cluster with the image specified by `IMG`:**

```sh
make deploy IMG=<some-registry>/namespaceclass-controller:tag
```

> **NOTE**: If you encounter RBAC errors, you may need to grant yourself cluster-admin
privileges or be logged in as admin.

**Create instances of your solution**
You can apply the samples (examples) from the config/sample:

```sh
kubectl apply -k config/samples/
```

>**NOTE**: Ensure that the samples has default values to test it out.

### To Uninstall
**Delete the instances (CRs) from the cluster:**

```sh
kubectl delete -k config/samples/
```

**Delete the APIs(CRDs) from the cluster:**

```sh
make uninstall
```

**UnDeploy the controller from the cluster:**

```sh
make undeploy
```

## Project Distribution

Following the options to release and provide this solution to the users.

### By providing a bundle with all YAML files

1. Build the installer for the image built and published in the registry:

```sh
make build-installer IMG=<some-registry>/namespaceclass-controller:tag
```

**NOTE:** The makefile target mentioned above generates an 'install.yaml'
file in the dist directory. This file contains all the resources built
with Kustomize, which are necessary to install this project without its
dependencies.

2. Using the installer

Users can just run 'kubectl apply -f <URL for YAML BUNDLE>' to install
the project, i.e.:

```sh
kubectl apply -f https://raw.githubusercontent.com/<org>/namespaceclass-controller/<tag or branch>/dist/install.yaml
```

### By providing a Helm Chart

The chart lives under `dist/chart` and is regenerated with `make helm-generate`
whenever the CRDs or RBAC change. Releasing it (building the image, packaging
the chart, and pushing both to GHCR) happens automatically on every `v*.*.*`
tag push — see `.github/workflows/release.yml` and the
[Quick Install](#quick-install) section above. To do the same thing locally:

```sh
make docker-build docker-push IMG=ghcr.io/<you>/namespaceclass-controller:<version>
make helm-package VERSION=<version> IMG=ghcr.io/<you>/namespaceclass-controller:<version>
make helm-push VERSION=<version> HELM_OCI_REGISTRY=oci://ghcr.io/<you>/charts
```

## Contributing
// TODO(user): Add detailed information on how you would like others to contribute to this project

**NOTE:** Run `make help` for more information on all potential `make` targets

More information can be found via the [Kubebuilder Documentation](https://book.kubebuilder.io/introduction.html)

## License

Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

