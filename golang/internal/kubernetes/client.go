package kubernetes

import (
	"fmt"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// defaultReplicas is what Kubernetes assumes when a Deployment manifest omits
// spec.replicas.
const defaultReplicas int32 = 1

// defaultExemptNamespaces are namespaces where blanket isolation would break
// the cluster rather than protect it. They need hand-written policies instead.
var defaultExemptNamespaces = []string{
	"kube-system",
	"kube-public",
	"kube-node-lease",
}

// ClientConfig configures a KubernetesClient. A struct rather than positional
// arguments, so adding options later is not a breaking change.
type ClientConfig struct {
	// Kubeconfig is a path to a kubeconfig file. Empty means in-cluster.
	Kubeconfig string

	ExemptNamespaces []string
}

// KubernetesClient talks to a cluster. It knows nothing about HTTP.
type KubernetesClient struct {
	Clientset kubernetes.Interface
	// Dynamic addresses CRDs — Cilium policies — without vendoring their Go
	// module.
	Dynamic            dynamic.Interface
	ExemptedNamespaces map[string]bool
	Version            string
}

// ClusterHealth summarises whether every Deployment has as many ready replicas
// as it desires.
type ClusterHealth struct {
	Healthy   bool                  `json:"healthy"`
	Total     int                   `json:"total_deployments"`
	Unhealthy []UnhealthyDeployment `json:"unhealthy,omitempty"`
}

// UnhealthyDeployment names a single Deployment that is short of replicas.
type UnhealthyDeployment struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Desired   int32  `json:"desired_replicas"`
	Ready     int32  `json:"ready_replicas"`
}

// NewKubernetesClient builds a client from cfg. An empty Kubeconfig falls back
// to in-cluster configuration.
func NewKubernetesClient(cfg ClientConfig) (*KubernetesClient, error) {
	kConfig, err := clientcmd.BuildConfigFromFlags("", cfg.Kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("building config: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(kConfig)
	if err != nil {
		return nil, fmt.Errorf("creating clientset: %w", err)
	}

	dynamicClient, err := dynamic.NewForConfig(kConfig)
	if err != nil {
		return nil, fmt.Errorf("creating dynamic client: %w", err)
	}

	version, err := GetKubernetesVersion(clientset)
	if err != nil {
		return nil, fmt.Errorf("fetching server version: %w", err)
	}

	exemptNames := cfg.ExemptNamespaces
	if exemptNames == nil {
		exemptNames = defaultExemptNamespaces
	}

	exempt := make(map[string]bool, len(exemptNames))
	for _, name := range exemptNames {
		exempt[name] = true
	}

	return &KubernetesClient{
		Clientset:          clientset,
		Dynamic:            dynamicClient,
		ExemptedNamespaces: exempt,
		Version:            version,
	}, nil
}
