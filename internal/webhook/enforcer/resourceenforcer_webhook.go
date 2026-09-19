package enforcer

import (
	"context"
	"fmt"

	"github.com/atgardner/namespaceclass-controller/internal/common"
	jsonpatch "github.com/evanphx/json-patch/v5"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	namespaceclassv1alpha1 "github.com/atgardner/namespaceclass-controller/api/v1alpha1"
)

type ResourceEnforcer struct {
	Client  client.Client
	decoder admission.Decoder
}

func SetupResourceEnforcerWebhookWithManager(mgr ctrl.Manager) {
	mgr.GetWebhookServer().Register(
		"/mutate-namespaceclass-managed-resources",
		&webhook.Admission{Handler: &ResourceEnforcer{
			Client:  mgr.GetClient(),
			decoder: admission.NewDecoder(mgr.GetScheme()),
		}},
	)
}

func (r *ResourceEnforcer) Handle(ctx context.Context, req admission.Request) admission.Response {
	log := logf.FromContext(ctx)

	desired, err := r.getDesiredResource(ctx, req)
	if err != nil {
		log.Error(err, "failed to get desired resource", "name", req.Name)
		return admission.Allowed("")
	}

	if desired == nil {
		return admission.Allowed("")
	}

	desiredJson, err := desired.MarshalJSON()
	if err != nil {
		log.Error(err, "failed to marshal resolved resource to JSON", "name", req.Name)
		return admission.Allowed("")
	}

	mergedJSON, err := jsonpatch.MergePatch(req.Object.Raw, desiredJson)
	if err != nil {
		log.Error(err, "failed to merge patch", "name", req.Name)
		return admission.Allowed("")
	}

	resp := admission.PatchResponseFromRaw(req.Object.Raw, mergedJSON)
	if len(resp.Patches) == 0 {
		return resp
	}

	log.Info("Resource enforcement applied", "name", req.Name, "namespace", req.Namespace, "patches", resp.Patches)

	warnings := make(admission.Warnings, 0, len(resp.Patches))
	for _, p := range resp.Patches {
		warnings = append(warnings, fmt.Sprintf("namespaceclass-controller reverted %s %s", p.Operation, p.Path))
	}

	return resp.WithWarnings(warnings...)
}

func (r *ResourceEnforcer) getDesiredResource(ctx context.Context, req admission.Request) (*unstructured.Unstructured, error) {
	content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(req.Object.Object)
	if err != nil {
		return nil, fmt.Errorf("failed to convert object to unstructured: %w", err)
	}

	obj := &unstructured.Unstructured{Object: content}
	nsClassName := obj.GetLabels()[common.ParentClassLabel]
	if nsClassName == "" {
		return nil, nil
	}

	nsClass := &namespaceclassv1alpha1.NamespaceClass{}
	if err := r.Client.Get(ctx, client.ObjectKey{Name: nsClassName}, nsClass); err != nil {
		return nil, fmt.Errorf("failed to get namespace class: %w", err)
	}

	gvk := obj.GroupVersionKind()
	for _, res := range nsClass.Spec.Resources {
		if gvk == res.GroupVersionKind() && obj.GetName() == res.GetName() {
			rb := common.NewResourceBuilder(r.Client)
			desired, err := rb.BuildFinalResource(&res, req.Namespace, nsClass)
			if err != nil {
				return nil, fmt.Errorf("failed to build final resource: %w", err)
			}

			return desired, nil
		}
	}

	return nil, nil
}
