package common

// namespaceClassLabel marks a Namespace as managed by a NamespaceClass.
const NamespaceClassLabel = "namespaceclass.akuity.io/name"

// parentClassLabel marks each resource with the NamespaceClass that createad it.
const ParentClassLabel = "namespaceclass.akuity.io/parent"

// fieldOwner identifies this controller as the Server-Side Apply field
// manager for resources templated from a NamespaceClass.
const FieldOwner = "namespaceclass-controller"

// appliedResourcesAnnotation records, on the Namespace, the GVK+name of every
// resource this controller applied on its last successful reconcile. It's
// the source of truth for the diff — not a label selector — because a
// resource can be dropped from a class's spec (or the namespace can switch
// classes) without leaving any current signal behind to find it by.
const AppliedResourcesAnnotation = "namespaceclass.akuity.io/applied-resources"

const ReconcileErrorAnnotation = "namespaceclass.akuity.io/reconcile-error"

const NamespaceClassFinalizer = "namespaceclass.akuity.io/finalizer"
