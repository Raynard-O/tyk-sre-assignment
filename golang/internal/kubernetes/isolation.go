package kubernetes

import (
	"context"
	"errors"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
)

var (
	ErrNotFound      = errors.New("isolation not found")
	ErrAlreadyExists = errors.New("isolation already exists")
)

// ciliumPolicyResource
var ciliumPolicyResource = schema.GroupVersionResource{
	Group:    "cilium.io",
	Version:  "v2",
	Resource: "ciliumclusterwidenetworkpolicies",
}

const namespaceLabel = "k8s:io.kubernetes.pod.namespace"

// Workload identifies one side of an isolation: pods carrying all of Labels, in
// any of Namespaces.
type Workload struct {
	Namespaces []string          `json:"namespaces"`
	Labels     map[string]string `json:"labels"`
}

// Isolation is a request to stop two workloads exchanging network traffic.
type Isolation struct {
	// Name identifies the policy. An incident ID maybe or something unique
	// it is what Release takes

	Name string   `json:"name"`
	A    Workload `json:"a"`
	B    Workload `json:"b"`
}

// Validate reports whether the request describes the cut the caller intended.
func (i Isolation) Validate() error {
	if errs := validation.IsDNS1123Subdomain(i.Name); len(errs) > 0 {
		return fmt.Errorf("invalid name %q: %s", i.Name, strings.Join(errs, "; "))
	}

	if err := i.A.validate("a"); err != nil {
		return err
	}

	return i.B.validate("b")
}

func (w Workload) validate(side string) error {
	if len(w.Namespaces) == 0 {
		return fmt.Errorf("workload %s: at least one namespace is required", side)
	}

	for _, namespace := range w.Namespaces {
		if errs := validation.IsDNS1123Label(namespace); len(errs) > 0 {
			return fmt.Errorf("workload %s: invalid namespace %q: %s", side, namespace, strings.Join(errs, "; "))
		}
	}

	if len(w.Labels) == 0 {
		return fmt.Errorf("workload %s: at least one label is required", side)
	}

	for key, value := range w.Labels {
		if errs := validation.IsQualifiedName(key); len(errs) > 0 {
			return fmt.Errorf("workload %s: invalid label key %q: %s", side, key, strings.Join(errs, "; "))
		}

		if errs := validation.IsValidLabelValue(value); len(errs) > 0 {
			return fmt.Errorf("workload %s: invalid label value %q: %s", side, value, strings.Join(errs, "; "))
		}
	}

	return nil
}

func (w Workload) selector() map[string]any {
	namespaces := make([]any, 0, len(w.Namespaces))
	for _, namespace := range w.Namespaces {
		namespaces = append(namespaces, namespace)
	}

	labels := make(map[string]any, len(w.Labels))
	for key, value := range w.Labels {
		labels[key] = value
	}

	return map[string]any{
		"matchLabels": labels,
		"matchExpressions": []any{
			map[string]any{
				"key":      namespaceLabel,
				"operator": "In",
				"values":   namespaces,
			},
		},
	}
}

// policy renders the isolation as a CiliumClusterwideNetworkPolicy.
func (i Isolation) policy() *unstructured.Unstructured {
	peer := []any{i.B.selector()}

	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "cilium.io/v2",
		"kind":       "CiliumClusterwideNetworkPolicy",
		"metadata": map[string]any{
			"name": i.Name,
		},
		"spec": map[string]any{
			"endpointSelector": i.A.selector(),
			"enableDefaultDeny": map[string]any{
				"ingress": false,
				"egress":  false,
			},
			"egressDeny": []any{
				map[string]any{"toEndpoints": peer},
			},
			"ingressDeny": []any{
				map[string]any{"fromEndpoints": peer},
			},
		},
	}}
}

// Isolate stops all network activity between the two workloads.
func (k *KubernetesClient) Isolate(ctx context.Context, isolation Isolation) error {
	_, err := k.Dynamic.Resource(ciliumPolicyResource).
		Create(ctx, isolation.policy(), metav1.CreateOptions{})

	switch {
	case apierrors.IsAlreadyExists(err):
		return ErrAlreadyExists
	case err != nil:
		return fmt.Errorf("creating cilium policy %s: %w", isolation.Name, err)
	}

	return nil
}

// Release restores traffic between a previously isolated pair.
func (k *KubernetesClient) Release(ctx context.Context, name string) error {
	err := k.Dynamic.Resource(ciliumPolicyResource).
		Delete(ctx, name, metav1.DeleteOptions{})

	switch {
	case apierrors.IsNotFound(err):
		return ErrNotFound
	case err != nil:
		return fmt.Errorf("deleting cilium policy %s: %w", name, err)
	}

	return nil
}
