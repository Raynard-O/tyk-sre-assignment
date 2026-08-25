package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/TykTechnologies/tyk-sre-assignment/internal/kubernetes"
)

type stubKube struct {
	pingErr    error
	health     kubernetes.ClusterHealth
	healthErr  error
	isolateErr error
	releaseErr error

	// Captured so tests can assert what the handler passed down.
	gotIsolation kubernetes.Isolation
	gotRelease   string
}

func (s *stubKube) Ping(context.Context) error { return s.pingErr }

func (s *stubKube) DeploymentHealth(context.Context) (kubernetes.ClusterHealth, error) {
	return s.health, s.healthErr
}

func (s *stubKube) Isolate(_ context.Context, isolation kubernetes.Isolation) error {
	s.gotIsolation = isolation

	return s.isolateErr
}

func (s *stubKube) Release(_ context.Context, name string) error {
	s.gotRelease = name

	return s.releaseErr
}

func newTestServer(kube kubeClient) *server {
	return newServer(kube, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

const validBody = `{
  "name": "inc-4821",
  "a": {"namespaces": ["team-a"], "labels": {"app": "checkout"}},
  "b": {"namespaces": ["team-b"], "labels": {"app": "payments"}}
}`

func postIsolation(srv *server, body string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	srv.handleIsolate(recorder, httptest.NewRequest(http.MethodPost, "/isolations", strings.NewReader(body)))

	return recorder
}

func deleteIsolation(srv *server, name string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodDelete, "/isolations/"+name, nil)

	request.SetPathValue("name", name)

	recorder := httptest.NewRecorder()
	srv.handleRelease(recorder, request)

	return recorder
}

func TestIsolateSucceeds(t *testing.T) {
	t.Parallel()

	kube := &stubKube{}

	recorder := postIsolation(newTestServer(kube), validBody)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d, body: %s", recorder.Code, http.StatusCreated, recorder.Body)
	}

	if contentType := recorder.Header().Get("Content-Type"); contentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", contentType)
	}
}

func TestIsolatePassesTheRequestThroughUnchanged(t *testing.T) {
	t.Parallel()

	kube := &stubKube{}

	if recorder := postIsolation(newTestServer(kube), validBody); recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusCreated)
	}

	got := kube.gotIsolation

	if got.Name != "inc-4821" {
		t.Errorf("name = %q, want inc-4821", got.Name)
	}

	if len(got.A.Namespaces) != 1 || got.A.Namespaces[0] != "team-a" {
		t.Errorf("a.namespaces = %v, want [team-a]", got.A.Namespaces)
	}

	if got.A.Labels["app"] != "checkout" {
		t.Errorf("a.labels = %v, want app=checkout", got.A.Labels)
	}

	if got.B.Labels["app"] != "payments" {
		t.Errorf("b.labels = %v, want app=payments", got.B.Labels)
	}
}

func TestIsolateRejectsMalformedJSON(t *testing.T) {
	t.Parallel()

	kube := &stubKube{}

	recorder := postIsolation(newTestServer(kube), `{"name": "inc-1",`)

	if recorder.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}

	if kube.gotIsolation.Name != "" {
		t.Error("handler called Isolate despite a malformed body")
	}
}

func TestIsolateRejectsInvalidRequest(t *testing.T) {
	t.Parallel()

	kube := &stubKube{}

	// Side B has no labels, which would otherwise isolate the whole namespace.
	body := `{
	  "name": "inc-4821",
	  "a": {"namespaces": ["team-a"], "labels": {"app": "checkout"}},
	  "b": {"namespaces": ["team-b"]}
	}`

	recorder := postIsolation(newTestServer(kube), body)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}

	if !strings.Contains(recorder.Body.String(), "label") {
		t.Errorf("body = %s, want it to explain the validation failure", recorder.Body)
	}

	if kube.gotIsolation.Name != "" {
		t.Error("handler called Isolate despite failing validation")
	}
}

func TestIsolateRejectsOversizedBody(t *testing.T) {
	t.Parallel()

	kube := &stubKube{}

	body := `{"name":"inc-1","a":{"namespaces":["team-a"],"labels":{"app":"` +
		strings.Repeat("x", maxRequestBody+1) + `"}}}`

	recorder := postIsolation(newTestServer(kube), body)

	if recorder.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
}

func TestIsolateAlreadyExistsIsAConflict(t *testing.T) {
	t.Parallel()

	kube := &stubKube{isolateErr: kubernetes.ErrAlreadyExists}

	recorder := postIsolation(newTestServer(kube), validBody)

	if recorder.Code != http.StatusConflict {
		t.Errorf("status = %d, want %d", recorder.Code, http.StatusConflict)
	}
}

func TestIsolateUpstreamFailureIsBadGateway(t *testing.T) {
	t.Parallel()

	kube := &stubKube{isolateErr: errors.New("etcd request timed out")}

	recorder := postIsolation(newTestServer(kube), validBody)

	// The dependency failed, not us.
	if recorder.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want %d", recorder.Code, http.StatusBadGateway)
	}

	if strings.Contains(recorder.Body.String(), "etcd") {
		t.Errorf("body = %s, leaks the upstream error", recorder.Body)
	}
}

func TestReleaseSucceeds(t *testing.T) {
	t.Parallel()

	kube := &stubKube{}

	recorder := deleteIsolation(newTestServer(kube), "inc-4821")

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
	}

	if kube.gotRelease != "inc-4821" {
		t.Errorf("released %q, want inc-4821", kube.gotRelease)
	}

	if recorder.Body.Len() != 0 {
		t.Errorf("204 response has a body: %s", recorder.Body)
	}
}

func TestReleaseUnknownIsolationIsNotFound(t *testing.T) {
	t.Parallel()

	kube := &stubKube{releaseErr: kubernetes.ErrNotFound}

	recorder := deleteIsolation(newTestServer(kube), "never-existed")

	if recorder.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", recorder.Code, http.StatusNotFound)
	}
}

func TestReleaseUpstreamFailureIsBadGateway(t *testing.T) {
	t.Parallel()

	kube := &stubKube{releaseErr: errors.New("connection refused")}

	recorder := deleteIsolation(newTestServer(kube), "inc-4821")

	if recorder.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want %d", recorder.Code, http.StatusBadGateway)
	}
}

func TestHealthDoesNotDependOnKubernetes(t *testing.T) {
	t.Parallel()

	srv := newTestServer(&stubKube{
		pingErr:   errors.New("down"),
		healthErr: errors.New("down"),
	})

	recorder := httptest.NewRecorder()
	srv.healthHandler(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if recorder.Code != http.StatusOK {
		t.Errorf("healthz = %d, want %d", recorder.Code, http.StatusOK)
	}

	recorder = httptest.NewRecorder()
	srv.readyHandler(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if recorder.Code != http.StatusServiceUnavailable {
		t.Errorf("readyz = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
}

func TestReadySucceedsWhenKubernetesIsReachable(t *testing.T) {
	t.Parallel()

	srv := newTestServer(&stubKube{})

	recorder := httptest.NewRecorder()
	srv.readyHandler(
		recorder,
		httptest.NewRequest(http.MethodGet, "/readyz", nil),
	)

	if recorder.Code != http.StatusOK {
		t.Errorf("readyz = %d, want %d", recorder.Code, http.StatusOK)
	}
}

func TestClusterHealthHealthy(t *testing.T) {
	t.Parallel()

	srv := newTestServer(&stubKube{
		health: kubernetes.ClusterHealth{
			Healthy: true,
			Total:   2,
		},
	})

	recorder := httptest.NewRecorder()

	srv.handleClusterHealth(
		recorder,
		httptest.NewRequest(http.MethodGet, "/cluster/health", nil),
	)

	if recorder.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
}
