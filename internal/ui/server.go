// Package ui implements the ratchet web interface: a read-mostly dashboard
// with an escalation handler for human-in-the-loop job resolution.
package ui

import (
	"context"
	"embed"
	"html/template"
	"log/slog"
	"net/http"

	"ratchet/internal/db"
)

//go:embed templates
var templateFS embed.FS

type server struct {
	db   *db.DB
	mux  *http.ServeMux
	tmpl *templateCache
}

// templateCache holds pre-parsed template sets keyed by page name.
type templateCache struct {
	dashboard   *template.Template
	escalations *template.Template
	escalation  *template.Template
	trace       *template.Template
}

func newTemplateCache() (*templateCache, error) {
	parse := func(pages ...string) (*template.Template, error) {
		files := append([]string{"templates/layout.html"}, pages...)
		return template.ParseFS(templateFS, files...)
	}
	dashboard, err := parse("templates/dashboard.html")
	if err != nil {
		return nil, err
	}
	escalations, err := parse("templates/escalations.html")
	if err != nil {
		return nil, err
	}
	escalation, err := parse("templates/escalation.html")
	if err != nil {
		return nil, err
	}
	trace, err := parse("templates/trace.html")
	if err != nil {
		return nil, err
	}
	return &templateCache{
		dashboard:   dashboard,
		escalations: escalations,
		escalation:  escalation,
		trace:       trace,
	}, nil
}

func newServer(database *db.DB) (*server, error) {
	tmpl, err := newTemplateCache()
	if err != nil {
		return nil, err
	}
	s := &server{db: database, mux: http.NewServeMux(), tmpl: tmpl}
	s.routes()
	return s, nil
}

func (s *server) routes() {
	s.mux.HandleFunc("GET /{$}", s.handleDashboard)
	s.mux.HandleFunc("GET /hx/status", s.handleStatusPartial)
	s.mux.HandleFunc("GET /escalations", s.handleEscalations)
	s.mux.HandleFunc("GET /escalations/{id}", s.handleEscalationDetail)
	s.mux.HandleFunc("POST /escalations/{id}/requeue", s.handleRequeue)
	s.mux.HandleFunc("POST /escalations/{id}/close", s.handleClose)
	s.mux.HandleFunc("POST /projects/{id}/close", s.handleCloseProject)
	s.mux.HandleFunc("GET /trace", s.handleTrace)
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// Run starts the UI web server and blocks until ctx is cancelled.
func Run(ctx context.Context, database *db.DB, addr string) error {
	s, err := newServer(database)
	if err != nil {
		return err
	}

	srv := &http.Server{Addr: addr, Handler: s}

	go func() {
		<-ctx.Done()
		_ = srv.Shutdown(context.Background())
	}()

	slog.Info("ratchet ui listening", "addr", addr)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		return err
	}
	return nil
}
