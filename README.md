# tyk-sre-assignment

A small HTTP service that reports on Kubernetes cluster state and can cut network
traffic between two workloads on demand.

- **Cluster health** — which Deployments have fewer ready replicas than they want
- **Workload isolation** — stop two workloads exchanging any network traffic, and
  restore it later
- **Probes** — liveness and readiness, split so an API server outage does not get
  every replica restarted

---

## Endpoints

| Method | Path | Purpose | Success | Failure |
|---|---|---|---|---|
| `GET` | `/healthz` | Liveness. Does not touch Kubernetes. | `200` | — |
| `GET` | `/readyz` | Readiness. Checks the API server is reachable. | `200` | `503` |
| `GET` | `/cluster/health` | Deployment replica health, cluster-wide. | `200` | `503` unhealthy, `502` API error |
| `POST` | `/isolations` | Cut traffic between two workloads. | `201` | `400` invalid, `409` exists, `502` API error |
| `DELETE` | `/isolations/{name}` | Restore traffic. | `204` | `404` unknown, `502` API error |

### `GET /healthz`

Liveness only — it reports that the process is running and deliberately does
**not** call Kubernetes.

```console
$ curl -s localhost:8080/healthz
{"status":"ok"}
```

### `GET /readyz`

Readiness — whether the service can still reach the API server, and so whether it
should receive traffic.

```console
$ curl -s localhost:8080/readyz
{"status":"ready"}

# API server unreachable → 503
{"error":"kubernetes api unreachable"}
```

### `GET /cluster/health`

Whether every Deployment in the cluster has as many ready replicas as its spec
asks for, listing any that fall short. The status code carries the verdict too, so
a probe or alert does not have to parse the body.

```console
$ curl -s localhost:8080/cluster/health | jq
{
  "healthy": false,
  "total_deployments": 24,
  "unhealthy": [
    {
      "namespace": "team-b",
      "name": "payments",
      "desired_replicas": 3,
      "ready_replicas": 1
    }
  ]
}
```

### `POST /isolations`

Stops two workloads exchanging **any** network traffic. Each side is a set of
namespaces plus a label selector.

Requires Cilium: upstream `NetworkPolicy` is allow-only and cannot express a
targeted deny without also cutting off everything else those workloads talk to.

```console
$ curl -s -X POST localhost:8080/isolations \
    -H 'Content-Type: application/json' \
    -d '{
      "name": "inc-4821",
      "a": {"namespaces": ["team-a"], "labels": {"app": "checkout"}},
      "b": {"namespaces": ["team-b"], "labels": {"app": "payments"}}
    }'
{"status":"isolated"}
```

### `DELETE /isolations/{name}`

```console
$ curl -s -o /dev/null -w '%{http_code}\n' -X DELETE localhost:8080/isolations/inc-4821
204
```

---

## Quick start

The Go module lives in `golang/`.

### Run locally against a real cluster

```console
cd golang

go mod tidy
go build

./tyk-sre-assignment --kubeconfig ~/.kube/config --address :8080
```

Verify it is up:

```console
curl -s localhost:8080/healthz
curl -s localhost:8080/cluster/health | jq
```

### Run the tests

```console
cd golang

go vet ./...
go test -race -count=1 ./...
```

### Run the container

```console
docker build -t tyk-sre-assignment ./golang

docker run --rm -p 8080:8080 \
  -v "$HOME/.kube/config:/kubeconfig:ro" \
  tyk-sre-assignment --kubeconfig /kubeconfig --address :8080
```

Images are published to GHCR on every push to `main`:

```console
docker pull ghcr.io/<owner>/tyk-sre-assignment:latest
```

### Deploy with Helm

```console
helm upgrade --install tyk-sre ./charts/tyk-sre-assignment \
  --namespace platform --create-namespace \
  --set image.tag=<commit-sha>
```

Then port-forward and hit it:

```console
kubectl -n platform port-forward svc/tyk-sre-tyk-sre-assignment 8080:8080
curl -s localhost:8080/cluster/health | jq
```

The chart creates a ServiceAccount, ClusterRole and ClusterRoleBinding. In-cluster
credentials are picked up automatically, so `--kubeconfig` is left empty.

---

## Configuration

Command-line flags:

| Flag | Default | Purpose |
|---|---|---|
| `--address` | `:8080` | HTTP listen address |
| `--kubeconfig` | `""` | Path to a kubeconfig. Empty means in-cluster. |

Helm values:

| Value | Default | Purpose |
|---|---|---|
| `replicaCount` | `1` | Number of replicas |
| `image.repository` | `ghcr.io/raynard-o/tyk-sre-assignment` | Image repository |
| `image.tag` | `""` | Image tag. Falls back to `.Chart.AppVersion`. |
| `image.pullPolicy` | `IfNotPresent` | Pull policy |
| `service.type` | `ClusterIP` | Service type |
| `service.port` | `8080` | Service port |
| `resources` | requests set | Container resources |
| `rbac.create` | `true` | Create ClusterRole and binding |
| `rbac.isolation` | `true` | Grant write access to Cilium policies |
| `serviceAccount.create` | `true` | Create a ServiceAccount |
| `args` | `["--address=:8080"]` | Extra container arguments |

### RBAC

The service needs read access cluster-wide, and write access to Cilium policies
only if you use the isolation endpoints:

| Resource | Verbs | Needed by |
|---|---|---|
| `apps/deployments` | `list` | `/cluster/health` |
| `namespaces` | `list` | `/readyz` |
| `cilium.io/ciliumclusterwidenetworkpolicies` | `create`, `delete` | `/isolations` |

Set `rbac.isolation=false` to deploy a read-only instance. It then provably
cannot cut traffic, because it lacks the verbs rather than merely not exposing
them.