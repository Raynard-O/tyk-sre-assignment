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
**not** call Kubernetes. If it did, an API server blip would fail liveness on
every replica at once, kubelet would restart them all, and they still could not
reach the API server. Dependency failures belong in readiness, where the
consequence is "stop sending traffic" rather than "kill it".

```console
$ curl -s localhost:8080/healthz
{"status":"ok"}
```

### `GET /readyz`

Readiness — whether we can still reach the API server, and so whether we should
receive traffic.

```console
$ curl -s localhost:8080/readyz
{"status":"ready"}

# API server unreachable → 503
{"error":"kubernetes api unreachable"}
```

### `GET /cluster/health`

Lists every Deployment in the cluster and reports any with fewer ready replicas
than desired. The status code carries the verdict too, so a probe or alert does
not have to parse the body.

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

Deployments that omit `spec.replicas` are treated as wanting 1, matching
Kubernetes' own default. (`spec.replicas` is a `*int32` and is `nil` when unset —
dereferencing it blindly panics.)

### `POST /isolations`

Stops two workloads exchanging **any** network traffic. Each side is a set of
namespaces plus a label selector.

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

`name` identifies the isolation and is what you pass to `DELETE` — an incident ID
makes a good one. Both sides require at least one namespace and at least one
label: an empty selector would match every pod in those namespaces, turning
"isolate one Deployment" into "isolate everything the team runs".

**This requires Cilium.** Upstream `NetworkPolicy` is allow-only — a pod selected
by a policy is isolated for that direction and only explicitly-allowed traffic
gets through. There is no way to express "deny just this pair" without also
cutting off everything else those workloads talk to. Cilium's `ingressDeny` /
`egressDeny` can, and its deny rules take precedence over every allow rule,
including plain NetworkPolicies.

The service creates one `CiliumClusterwideNetworkPolicy` per isolation. It selects
side A only, which covers both directions: A is an endpoint of every A↔B packet,
so `egressDeny` stops A→B at the source and `ingressDeny` stops B→A at the
destination. It also sets `enableDefaultDeny: {ingress: false, egress: false}` —
without that, selecting A would put A into default-deny and cut it off from the
entire cluster rather than just from B.

### `DELETE /isolations/{name}`

```console
$ curl -s -o /dev/null -w '%{http_code}\n' -X DELETE localhost:8080/isolations/inc-4821
204
```

---

## Quick start

### Run locally against a real cluster

```console
go mod tidy
go build -o tyk-sre-assignment ./cmd/app

./tyk-sre-assignment --kubeconfig ~/.kube/config --address :8080
```

Verify it is up:

```console
curl -s localhost:8080/healthz
curl -s localhost:8080/cluster/health | jq
```

### Run the container

```console
docker build -t tyk-sre-assignment .

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

---
