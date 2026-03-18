package api

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/enjoys-in/go-mta/pkg/logger"
	"github.com/enjoys-in/go-mta/pkg/server"
)

// API is the HTTP server that exposes the MTA endpoints.
type API struct {
	srv  *server.Server
	http *http.Server
	log  *logger.Logger
	addr string
}

// New creates a new API wired to the given MTA server.
func New(srv *server.Server, addr string) *API {
	if addr == "" {
		addr = ":8080"
	}
	a := &API{
		srv:  srv,
		log:  logger.New("api"),
		addr: addr,
	}
	mux := http.NewServeMux()
	a.registerRoutes(mux)

	a.http = &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
		BaseContext: func(_ net.Listener) context.Context {
			return context.Background()
		},
	}
	return a
}

func (a *API) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/send", a.handleSend)
	mux.HandleFunc("GET /api/v1/health", a.handleHealth)
}

// ListenAndServe starts the HTTP server. Blocks until closed.
func (a *API) ListenAndServe() error {
	a.log.Info(fmt.Sprintf("API listening on %s", a.addr))
	err := a.http.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

// Shutdown gracefully shuts down the HTTP server.
func (a *API) Shutdown(ctx context.Context) error {
	a.log.Info("API shutting down")
	return a.http.Shutdown(ctx)
}
