package kubernetes

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// GetKubernetesVersion returns a string GitVersion of the Kubernetes server defined by the clientset.
//
// If it can't connect an error will be returned, which makes it useful to check connectivity.
func GetKubernetesVersion(clientset kubernetes.Interface) (string, error) {
	version, err := clientset.Discovery().ServerVersion()
	if err != nil {
		return "", err
	}

	return version.String(), nil
}

// Ping checks that the API server is still reachable. It lists a single
// namespace rather than calling discovery.
func (k *KubernetesClient) Ping(ctx context.Context) error {
	_, err := k.Clientset.CoreV1().
		Namespaces().
		List(ctx, metav1.ListOptions{Limit: 1})
	if err != nil {
		return fmt.Errorf("pinging api server: %w", err)
	}

	return nil
}

// DeploymentHealth reports every Deployment in the cluster that has fewer ready
// replicas than it desires.
func (k *KubernetesClient) DeploymentHealth(ctx context.Context) (ClusterHealth, error) {
	list, err := k.Clientset.AppsV1().
		Deployments(metav1.NamespaceAll).
		List(ctx, metav1.ListOptions{})
	if err != nil {
		return ClusterHealth{}, fmt.Errorf("listing deployments: %w", err)
	}

	health := ClusterHealth{Healthy: true, Total: len(list.Items)}

	for _, deployment := range list.Items {
		desired := defaultReplicas
		if deployment.Spec.Replicas != nil {
			desired = *deployment.Spec.Replicas
		}

		ready := deployment.Status.ReadyReplicas
		if ready < desired {
			health.Healthy = false
			health.Unhealthy = append(health.Unhealthy, UnhealthyDeployment{
				Namespace: deployment.Namespace,
				Name:      deployment.Name,
				Desired:   desired,
				Ready:     ready,
			})
		}
	}

	return health, nil
}
