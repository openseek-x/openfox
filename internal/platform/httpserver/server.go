package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"openfox/internal/platform/apierror"
	"openfox/internal/platform/config"
	"openfox/internal/platform/requestid"
)

type ReadinessCheck func(context.Context) error

func ReadinessNotConfigured(context.Context) error {
	return errors.New("database readiness adapter is not configured")
}

type Server struct {
	httpServer *http.Server
	mux        *http.ServeMux
}

func New(cfg config.Config, version string, logger *slog.Logger, readiness ReadinessCheck) *Server {
	if readiness == nil {
		readiness = ReadinessNotConfigured
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", metadataHandler(version))
	mux.HandleFunc("GET /live", liveHandler)
	mux.HandleFunc("GET /healthz", liveHandler)
	mux.HandleFunc("GET /ready", readyHandler(readiness))

	server := &http.Server{
		Addr:              cfg.Server.Listen,
		Handler:           requestid.Middleware(mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	if logger != nil {
		server.ErrorLog = slog.NewLogLogger(logger.Handler(), slog.LevelError)
	}
	return &Server{httpServer: server, mux: mux}
}

// Mount adds application routes before ListenAndServe. The server owns the
// outer request-ID middleware and the fixed health endpoints.
func (s *Server) Mount(pattern string, handler http.Handler) {
	if s == nil || s.mux == nil || handler == nil {
		return
	}
	s.mux.Handle(pattern, handler)
}

func (s *Server) ListenAndServe() error {
	return s.httpServer.ListenAndServe()
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

func metadataHandler(version string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"service": "openfox-server", "version": version})
	}
}

func liveHandler(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func readyHandler(check ReadinessCheck) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := check(r.Context()); err != nil {
			apierror.Write(w, http.StatusServiceUnavailable, "NOT_READY", "service dependencies are not ready", requestID(r))
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func requestID(r *http.Request) string {
	if id := requestid.FromContext(r.Context()); id != "" {
		return id
	}
	return r.Header.Get(requestid.Header)
}
