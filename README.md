# namespaceclass-controller
The namespaceclass-controller allows Kubernetes admins to define a set of
namespace "classes". Each such NamespaceClass can contain a list of resources,
that will be created and maintained in every namespace that references that
class:

```yaml
apiVersion: namespaceclass.akuity.io/v1alpha1
kind: NamespaceClass
metadata:
  name: public-network
spec:
  resources:
  - apiVersion: networking.k8s.io/v1
    kind: NetworkPolicy
    metadata:
      name: allow-public-ingress
    spec:
      podSelector: {}
      policyTypes:
      - Ingress
      ingress:
      - {} # no "from" restricts nothing: ingress allowed from any source, including the public internet
```

A Namespace will reference its NamespaceClass by setting a specific label:

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: web-portal
  labels:
    namespaceclass.akuity.io/name: public-network
```

The controller will make sure the resources in the class will be created and
kept up-to-date with the class definition.

## Quick Install

The controller image and Helm chart are published to GHCR on every tagged
release (see `.github/workflows/release.yml`). To run it in any cluster,
no local build required:

**Prerequisite:** the webhooks (the Helm chart's, the plain YAML bundle's,
and the Kustomize command's below) need TLS certs, provisioned via
[cert-manager](https://cert-manager.io). Install it first if the cluster
doesn't already have it:

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

To skip the cert-manager dependency entirely, pass
`--set certManager.enabled=false,webhook.enabled=false` — this disables both
webhooks (admission-time validation and real-time drift enforcement), not
just their certs; the reconciler's own reconcile-loop checks still cover the
same ground, just without the immediate feedback.

**Without Helm:** a plain YAML bundle (CRDs, RBAC, Deployment, and both
webhooks — same cert-manager prerequisite as above) is published alongside
every tag too:

```sh
kubectl apply -f https://raw.githubusercontent.com/atgardner/namespaceclass-controller/<tag>/dist/install.yaml
```

**With Kustomize:** build directly off that tag's `config/` tree instead,
overriding the image to the one actually published for it (the checked-in
`config/manager/kustomization.yaml` only points at a `controller:latest`
placeholder):

```sh
mkdir -p namespaceclass-controller-install && cd namespaceclass-controller-install
cat <<EOF > kustomization.yaml
resources:
  - github.com/atgardner/namespaceclass-controller/config/default?ref=<tag>
images:
  - name: controller
    newName: ghcr.io/atgardner/namespaceclass-controller
    newTag: <released-version>
EOF
kubectl apply -k .
```

Then apply a `NamespaceClass` and label a namespace with
`namespaceclass.akuity.io/name=<class-name>` — see `config/samples/`.

## Description

The project used the kubebuilder framework and the controller-runtime package
to define the NamespaceClass CRD, and its accompanying controllers and
webhooks.

### Main reconciliation loop

The NamespaceController makes sure each namespace that has the
`namespaceclass.akuity.io/name` label is kept up-to-date with its
NamespaceClass definition. Any change to the label value (switching between
classes, adding or removing the label), will create all required resources
from the "new" class, and delete all leftover resources from the "old" class
(if any). The namespace keeps a `namespaceclass.akuity.io/applied-resources`
annotation in order to keep track of the resources applied in the previous
reconciliation run, and compare against the current one to know which ones
need to be deleted.

The controller also listens on changes in any NamespaceClass
instance, in which case it will reconcile all Namespaces that reference that
class.

### Secondary reconciliation loop

The NamespaceClassReconciler maintains the `status` of a NamespaceClass. It
updates the number of failed Namespaces, and a condition describing the number
of successfully applied Namespaces and the general status of the
NamespaceClass.

### NamespaceClass admission webhook

The admission webhook validates that every resource defined in a NamespaceClass
`spec.resources` is a namespaced-scoped Kind. Any cluster-scoped Kind will
result in a failure to create the resource.

### ResourceEnforcer mutating webhook

The mutating webhook makes sure that whenever a client tries to edit a resource
that is managed by a NamespaceClass, its managed fields get re-applied by the
values defined in the class. It does nothing if there is no diff (=the incoming
change does not touch any of the managed fields), and applies the values +
returns a clear warning to the client if it does revert any incoming change.

### Resource Watcher

For every GVK any NamespaceClass references in its `spec.resources` list, a
watcher is being used. The watcher makes sure any future edits to resources
managed by a NamespaceClass will not alter fields explicitly set in the class's
spec.

Any such change will be immediately reverted. The watcher is a 2nd line
defence, since the enforcer mutating webhook should intercept those changes
before they reach etcd. But webhooks may temporarily become offline, or even
disabled completely in the helm chart.

## Design decisions

### Drift Correction

Resources are reconciled with Kubernetes [Server-Side Apply](https://kubernetes.io/docs/reference/using-api/server-side-apply/)
(`client.Apply` with `ForceOwnership`, this controller as the field manager).
That means drift correction is scoped to the fields a `NamespaceClass`
actually templates: if a `ConfigMap` key or label the class defines gets
hand-edited, the next reconcile reverts it. Fields the class never
mentioned — an extra label, an extra `data` key someone else added — are
left alone, because this controller never claimed ownership of them in the
first place.

This approach allows other tools to manage their own specific fields or
compliance labels, without fighting over with the NamespaceController. A
`NamespaceClass` resource is expected to share space with other legitimate
cluster tooling under this model, rather than assume it's the object's only
writer.

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

### Webhook `ignore` failure policy

Both webhooks have a `failurePolicy: ignore`, to make sure changes may reach
the cluster in case of a temporary network issue. Webhook servers can be
temporarily down, or unresponsive, and it shouldn't block users from managing
their clusters. The 2nd line of defence (described next) makes sure a bad-actor
will not be able to cause too much damage if the webhooks are down.

### Two enforcement layers

The admission-webhook makes sure no NamespaceClass is being created with a
cluster-scoped resource in its `spec.resources`, and the mutating-webhook makes
sure a client can't change any of the managed fields (of managed resources)
defined in the NamespaceClass. In case one or the other fails (or is even
disabled during the installation), a 2nd line of defence exists to protect
against these changes.

The NamespaceController makes sure to never apply *any* resources from a
NamespaceClass that has cluster-scoped resources, and the ResourceWatcher
watches any GVK that a NamespaceClass manages, and enforces its values to
remain as defined in its class whenever they change.

### Broad default RBAC

The controller requires a `*/*/*` RBAC role, because it should be able to
managed any (namespaced) kind in the cluster.

**Future work:** It might be worthwhile to allow the user to narrow down this
role, in case they require to manage a more specific set of resources.

### Wildcard mutating webhook

The ResourceEnforcer webhook is set on every GVK in the cluster, and would
trigger on every CREATE/UPDATE/DELETE of any resource, if not for the
ObjectSelector it contains - it filters by the existence of the
`namespaceclass.akuity.io/parent` label, thus only being triggered by resources
that are managed by a NamespaceClass.

### Cleanup mechanism

The NamespaceClass sets itself as the `ownerReference` for each resource it
manages. When the class is deleted, all of its managed resources (across all
Namespaces that reference the deleting class) will get deleted as well. The
NamespaceClass will get a finalizer, preventing it from being completely
removed from the cluster until all of its referencing Namespaces no longer
contain any managed resources. Only then would the finalizer be removed, and
the resource be completely deleted from the cluster.

## Getting Started

### Prerequisites
- go version v1.26+
- docker version 17.03+.
- kubectl version v1.19+.
- Access to a Kubernetes v1.19+ cluster.
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
You can apply the samples (examples) from the config/samples:

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

### Running tests

```sh
make test      # Unit + envtest suites (reconcilers, both webhooks, manager wiring) - no cluster needed
make test-e2e  # End-to-end suite against a real cluster - spins up its own isolated Kind cluster
```

`make test-e2e` provisions and tears down its own Kind cluster, so it's safe
to run alongside a real dev cluster without touching it.

## Releasing

Both distribution artifacts in [Quick Install](#quick-install) — the Helm
chart under `dist/chart` and the YAML bundle at `dist/install.yaml` — are
built from the same `config/` Kustomize tree and published automatically on
every `v*.*.*` tag push (see `.github/workflows/release.yml`). To reproduce
that locally, e.g. against your own registry:

```sh
make docker-build docker-push IMG=ghcr.io/<you>/namespaceclass-controller:<version>
make build-installer IMG=ghcr.io/<you>/namespaceclass-controller:<version>   # regenerates dist/install.yaml
make helm-generate                                                           # regenerates dist/chart, if config/ changed
make helm-package VERSION=<version> IMG=ghcr.io/<you>/namespaceclass-controller:<version>
make helm-push VERSION=<version> HELM_OCI_REGISTRY=oci://ghcr.io/<you>/charts
```

Run `make help` for the full list of available `make` targets.

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

