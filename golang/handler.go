// Place next to main.go
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/TykTechnologies/tyk-sre-assignment/internal/kubernetes"
)

// kubeClient is what the server needs from Kubernetes.
//
// Renamed from clusterProber: the last two methods change the cluster, so
// "prober" no longer described it.
type kubeClient interface {
	Ping(ctx context.Context) error
	DeploymentHealth(ctx context.Context) (kubernetes.ClusterHealth, error)
	Isolate(ctx context.Context, isolation kubernetes.Isolation) error
	Release(ctx context.Context, name string) error
}

// server holds the HTTP layer's dependencies explicitly rather than reaching
// for package-level globals.
type server struct {
	kube kubeClient
	log  *slog.Logger
}

// probeTimeout bounds how long any single handler will wait on the Kubernetes
// API before giving up.
const probeTimeout = 10 * time.Second

type statusResponse struct {
	Status string `json:"status"`
}

type errorResponse struct {
	Error string `json:"error"`
}

func newServer(kube kubeClient, log *slog.Logger) *server {
	return &server{kube: kube, log: log}
}

// writeJSON marshals before touching the ResponseWriter. Encoding straight to
// the writer would commit a 200 to the wire before a marshal error could be
// turned into a 500.
func (s *server) writeJSON(ctx context.Context, w http.ResponseWriter, status int, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		// Not http.Error: that would force Content-Type to text/plain on a
		// body that is JSON.
		s.log.ErrorContext(ctx, "marshalling response", "error", err)

		status = http.StatusInternalServerError
		body = []byte(`{"error":"internal server error"}`)
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)

	// The client may have hung up mid-write. Nothing to do but record it —
	// the status line is already sent.
	if _, err := w.Write(body); err != nil {
		s.log.WarnContext(ctx, "writing response body", "error", err)
	}
}
