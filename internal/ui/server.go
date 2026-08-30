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

// staticFS holds vendored front-end assets (htmx) served at /static/. Vendored
// rather than loaded from a CDN so the dashboard keeps working when the host
// has no internet route — the daemon commonly runs on a LAN next to Ollama.
//
//go:embed static
var staticFS embed.FS

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
	beadDetail  *template.Template
	trace       *template.Template
	report      *template.Template
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
	beadDetail, err := parse("templates/bead_detail.html")
	if err != nil {
		return nil, err
	}
	trace, err := parse("templates/trace.html")
	if err != nil {
		return nil, err
	}
	report, err := parse("templates/report.html")
	if err != nil {
		return nil, err
	}
	return &templateCache{
		dashboard:   dashboard,
		escalations: escalations,
		escalation:  escalation,
		beadDetail:  beadDetail,
		trace:       trace,
		report:      report,
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
	s.mux.Handle("GET /static/", cacheForever(http.FileServerFS(staticFS)))
	s.mux.HandleFunc("GET /{$}", s.handleDashboard)
	s.mux.HandleFunc("GET /hx/status", s.handleStatusPartial)
	s.mux.HandleFunc("GET /escalations", s.handleEscalations)
	s.mux.HandleFunc("GET /escalations/{id}", s.handleEscalationDetail)
	s.mux.HandleFunc("POST /escalations/{id}/requeue", s.handleRequeue)
	s.mux.HandleFunc("POST /escalations/{id}/requeue-with-budget", s.handleRequeuWithBudget)
	s.mux.HandleFunc("POST /escalations/{id}/close", s.handleClose)
	s.mux.HandleFunc("POST /escalations/{id}/grant-attempts", s.handleGrantAttempts)
	s.mux.HandleFunc("POST /escalations/{id}/rewind", s.handleRewindFromEscalation)
	s.mux.HandleFunc("POST /projects/{id}/close", s.handleCloseProject)
	s.mux.HandleFunc("POST /projects/{id}/resume", s.handleResumeProject)
	s.mux.HandleFunc("POST /projects/{id}/remove", s.handleRemoveProject)
	s.mux.HandleFunc("GET /beads/{id}", s.handleBeadDetail)
	s.mux.HandleFunc("POST /beads/{id}/rewind", s.handleRewindBead)
	s.mux.HandleFunc("GET /beads/{id}/report", s.handleBeadReport)
	s.mux.HandleFunc("GET /beads/{id}/snapshot/{n}", s.handleBeadSnapshot)
	s.mux.HandleFunc("GET /projects/{id}/report", s.handleProjectReport)
	s.mux.HandleFunc("GET /trace/{id}", s.handleTrace)
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// cacheForever marks a response as immutable for a year. The only assets under
// /static/ are version-pinned vendored libraries (htmx@2.0.4); bumping the
// version changes the filename, so an aggressive cache is safe.
func cacheForever(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		h.ServeHTTP(w, r)
	})
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
