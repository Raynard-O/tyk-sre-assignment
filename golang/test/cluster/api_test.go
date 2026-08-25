package cluster

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/TykTechnologies/tyk-sre-assignment/internal/kubernetes"
	"github.com/stretchr/testify/require"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func testSetup(t *testing.T) (*kubernetes.KubernetesClient, error) {
	t.Helper()

	client, err := kubernetes.NewKubernetesClient(kubernetes.ClientConfig{
		Kubeconfig:       os.Getenv("KUBECONFIG"),
		ExemptNamespaces: nil,
	})
	if err != nil {
		return nil, err
	}

	ctx := context.Background()

	namespaces := []string{
		"team-a",
		"team-b",
	}

	deployments := []struct {
		namespace string
		name      string
	}{
		{
			namespace: "team-a",
			name:      "app-a",
		},
		{
			namespace: "team-b",
			name:      "app-b",
		},
	}

	// Register cleanup before creating any resources.
	t.Cleanup(func() {
		cleanupCtx := context.Background()

		for _, deployment := range deployments {
			err := client.Clientset.
				AppsV1().
				Deployments(deployment.namespace).
				Delete(
					cleanupCtx,
					deployment.name,
					metav1.DeleteOptions{},
				)

			if err != nil {
				t.Logf(
					"failed to delete deployment %s/%s: %v",
					deployment.namespace,
					deployment.name,
					err,
				)
			}
		}

		for _, namespace := range namespaces {
			err := client.Clientset.
				CoreV1().
				Namespaces().
				Delete(
					cleanupCtx,
					namespace,
					metav1.DeleteOptions{},
				)

			if err != nil {
				t.Logf(
					"failed to delete namespace %s: %v",
					namespace,
					err,
				)
			}
		}
	})

	// Create namespaces.
	for _, namespace := range namespaces {
		_, err = client.Clientset.
			CoreV1().
			Namespaces().
			Create(
				ctx,
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: namespace,
					},
				},
				metav1.CreateOptions{},
			)

		if err != nil {
			return nil, err
		}
	}

	// Create deployments.
	for _, deployment := range deployments {
		replicas := int32(2)

		_, err = client.Clientset.
			AppsV1().
			Deployments(deployment.namespace).
			Create(
				ctx,
				&appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{
						Name: deployment.name,
					},
					Spec: appsv1.DeploymentSpec{
						Replicas: &replicas,

						Selector: &metav1.LabelSelector{
							MatchLabels: map[string]string{
								"app": deployment.name,
							},
						},

						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Labels: map[string]string{
									"app": deployment.name,
								},
							},
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{
									{
										Name:  "nginx",
										Image: "nginx:1.27",
									},
								},
							},
						},
					},
				},
				metav1.CreateOptions{},
			)

		if err != nil {
			return nil, err
		}
	}

	waitForDeploymentsReady(t, client, deployments)

	return client, nil
}

func waitForDeploymentsReady(
	t *testing.T,
	client *kubernetes.KubernetesClient,
	deployments []struct {
		namespace string
		name      string
	},
) {
	t.Helper()

	ctx := context.Background()

	require.Eventually(
		t,
		func() bool {
			for _, deployment := range deployments {
				d, err := client.Clientset.
					AppsV1().
					Deployments(deployment.namespace).
					Get(
						ctx,
						deployment.name,
						metav1.GetOptions{},
					)

				if err != nil {
					return false
				}

				if d.Spec.Replicas == nil {
					return false
				}

				if d.Status.ReadyReplicas != *d.Spec.Replicas {
					return false
				}
			}

			return true
		},
		30*time.Second,
		500*time.Millisecond,
	)
}

func TestDeploymentHealth(t *testing.T) {
	client, err := testSetup(t)
	require.NoError(t, err)

	ctx := context.Background()

	health, err := client.DeploymentHealth(ctx)

	require.NoError(t, err)
	require.True(t, health.Healthy)
	require.Empty(t, health.Unhealthy)
}
