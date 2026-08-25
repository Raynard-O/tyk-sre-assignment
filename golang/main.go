package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/TykTechnologies/tyk-sre-assignment/internal/kubernetes"
)

const maxRequestBody = 16 << 10 // 16 KiB

func main() {
	if err := run(); err != nil {
		slog.Error("startup failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	kubeconfig := flag.String("kubeconfig", "", "path to kubeconfig, leave empty for in-cluster")
	listenAddr := flag.String("address", ":8080", "HTTP server listen address")
	exempt := flag.String("exempt-namespaces", "",
		"comma-separated namespaces the isolation audit skips (default: kube-system,kube-public,kube-node-lease)")

	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	cfg := kubernetes.ClientConfig{Kubeconfig: *kubeconfig}
	if *exempt != "" {
		cfg.ExemptNamespaces = strings.Split(*exempt, ",")
	}

	kube, err := kubernetes.NewKubernetesClient(cfg)
	if err != nil {
		return fmt.Errorf("connecting to kubernetes: %w", err)
	}

	log.Info("connected to kubernetes", "version", kube.Version)

	return startServer(*listenAddr, newServer(kube, log))
}

// startServer launches an HTTP server with defined handlers and blocks until it's terminated or fails with an error.
//
// Expects a listenAddr to bind to.
func startServer(listenAddr string, s *server) error {
	http.HandleFunc("/healthz", s.healthHandler)
	http.HandleFunc("/readyz", s.readyHandler)
	http.HandleFunc("/cluster/health", s.handleClusterHealth)

	http.HandleFunc("POST /isolations", s.handleIsolate)
	http.HandleFunc("DELETE /isolations/{name}", s.handleRelease)

	fmt.Printf("Server listening on %s\n", listenAddr)

	return http.ListenAndServe(listenAddr, nil)
}

// healthHandler responds with the health status of the application.
// healthHandler is liveness: it reports only that this process is running.
func (s *server) healthHandler(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(r.Context(), w, http.StatusOK, statusResponse{Status: "ok"})
}

// readyHandler is readiness: it reports whether we can still reach the API
// server, and so whether we should receive traffic.
func (s *server) readyHandler(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), probeTimeout)
	defer cancel()

	if err := s.kube.Ping(ctx); err != nil {
		s.log.ErrorContext(ctx, "readiness probe failed", "error", err)
		s.writeJSON(ctx, w, http.StatusServiceUnavailable, errorResponse{Error: "kubernetes api unreachable"})

		return
	}

	s.writeJSON(ctx, w, http.StatusOK, statusResponse{Status: "ready"})
}

// handleClusterHealth reports Deployment replica health. The status
// code carries the verdict too
func (s *server) handleClusterHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), probeTimeout)
	defer cancel()

	health, err := s.kube.DeploymentHealth(ctx)
	if err != nil {
		s.log.ErrorContext(ctx, "cluster health check failed", "error", err)
		s.writeJSON(ctx, w, http.StatusBadGateway, errorResponse{Error: "could not query kubernetes api"})
		return
	}

	status := http.StatusOK
	if !health.Healthy {
		status = http.StatusServiceUnavailable
	}

	s.log.InfoContext(ctx, "cluster health checked",
		"healthy", health.Healthy,
		"total", health.Total,
		"unhealthy", len(health.Unhealthy),
	)

	s.writeJSON(ctx, w, status, health)
}

// handleIsolate stops two workloads exchanging any network traffic.
//
//	POST /isolations
//	{
//	  "name": "inc-4821",
//	  "a": {"namespaces": ["team-a"], "labels": {"app": "checkout"}},
//	  "b": {"namespaces": ["team-b"], "labels": {"app": "payments"}}
//	}
func (s *server) handleIsolate(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), probeTimeout)
	defer cancel()

	var isolation kubernetes.Isolation

	body := http.MaxBytesReader(w, r.Body, maxRequestBody)
	if err := json.NewDecoder(body).Decode(&isolation); err != nil {
		s.writeJSON(ctx, w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})

		return
	}

	// Validation detail is safe to return: it is about the caller's input.
	if err := isolation.Validate(); err != nil {
		s.writeJSON(ctx, w, http.StatusBadRequest, errorResponse{Error: err.Error()})

		return
	}

	switch err := s.kube.Isolate(ctx, isolation); {
	case errors.Is(err, kubernetes.ErrAlreadyExists):
		s.writeJSON(ctx, w, http.StatusConflict, errorResponse{Error: "isolation already exists"})

		return
	case err != nil:
		// Anything else is ours or the API server's, so log it and do not leak it.
		s.log.ErrorContext(ctx, "isolating workloads failed", "name", isolation.Name, "error", err)
		s.writeJSON(ctx, w, http.StatusBadGateway, errorResponse{Error: "could not apply network policy"})

		return
	}

	s.log.InfoContext(ctx, "workloads isolated", "name", isolation.Name)

	s.writeJSON(ctx, w, http.StatusCreated, statusResponse{Status: "isolated"})
}

// handleRelease restores traffic between a previously isolated pair.
//
//	DELETE /isolations/{name}
func (s *server) handleRelease(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), probeTimeout)
	defer cancel()

	name := r.PathValue("name")

	switch err := s.kube.Release(ctx, name); {
	case errors.Is(err, kubernetes.ErrNotFound):
		s.writeJSON(ctx, w, http.StatusNotFound, errorResponse{Error: "no such isolation"})

		return
	case err != nil:
		s.log.ErrorContext(ctx, "releasing isolation failed", "name", name, "error", err)
		s.writeJSON(ctx, w, http.StatusBadGateway, errorResponse{Error: "could not remove network policy"})

		return
	}

	s.log.InfoContext(ctx, "isolation released", "name", name)

	w.WriteHeader(http.StatusNoContent)
}
