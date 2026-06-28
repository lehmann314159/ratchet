package report

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// WriteProject generates and writes traces/project-report.md at project terminal state.
// File write failures are logged at WARN and do not propagate.
func WriteProject(ctx context.Context, tx *sql.Tx, projectID int64, folderPath string) {
	md, err := buildProjectReport(ctx, tx, projectID, folderPath)
	if err != nil {
		slog.Warn("post-execution: build project report", "project_id", projectID, "error", err)
		return
	}
	tracesDir := filepath.Join(folderPath, "traces")
	if err := os.MkdirAll(tracesDir, 0o755); err != nil {
		slog.Warn("post-execution: mkdir traces for project report", "error", err)
		return
	}
	path := filepath.Join(tracesDir, "project-report.md")
	if err := os.WriteFile(path, []byte(md), 0o644); err != nil {
		slog.Warn("post-execution: write project report", "project_id", projectID, "error", err)
	}
}

// ---- data types ----

type projectData struct {
	Label     string
	Status    string
	CreatedAt string
	UpdatedAt string
}

type beadSummary struct {
	BeadID    int64
	Title     string
	Status    string
	Attempts  int
	Revisions int
	WallTimeS int
}

// ---- query helpers ----

func queryProjectData(ctx context.Context, tx *sql.Tx, projectID int64) (projectData, error) {
	var p projectData
	err := tx.QueryRowContext(ctx, `
		SELECT label, status, created_at, updated_at
		FROM projects WHERE id = ?`, projectID).
		Scan(&p.Label, &p.Status, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

func queryBeadSummaries(ctx context.Context, tx *sql.Tx, projectID int64) ([]beadSummary, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT b.id, b.status,
		       COALESCE(br.full_text, ''),
		       (SELECT COUNT(*) FROM executions WHERE bead_id = b.id),
		       (SELECT COUNT(*) FROM bead_revisions WHERE bead_id = b.id),
		       COALESCE((
		           SELECT SUM(CAST(
		               (julianday(e.ended_at) - julianday(e.started_at)) * 86400 AS INTEGER
		           ))
		           FROM executions e
		           WHERE e.bead_id = b.id AND e.ended_at IS NOT NULL
		       ), 0)
		FROM beads b
		LEFT JOIN bead_revisions br ON br.id = b.current_revision_id
		WHERE b.project_id = ?
		ORDER BY b.id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []beadSummary
	for rows.Next() {
		var s beadSummary
		var fullText string
		if err := rows.Scan(&s.BeadID, &s.Status, &fullText,
			&s.Attempts, &s.Revisions, &s.WallTimeS); err != nil {
			return nil, err
		}
		s.Title, _, _ = parseBeadSpec(fullText)
		if s.Title == "" {
			s.Title = fmt.Sprintf("bead-%d", s.BeadID)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func queryEscalatedCount(ctx context.Context, tx *sql.Tx, projectID int64) int {
	var n int
	tx.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT bead_id) FROM handoff_jobs
		WHERE project_id = ? AND status = 'escalated' AND bead_id IS NOT NULL`,
		projectID).Scan(&n)
	return n
}

// ---- rendering ----

func buildProjectReport(ctx context.Context, tx *sql.Tx, projectID int64, folderPath string) (string, error) {
	proj, err := queryProjectData(ctx, tx, projectID)
	if err != nil {
		return "", fmt.Errorf("load project %d: %w", projectID, err)
	}
	beads, err := queryBeadSummaries(ctx, tx, projectID)
	if err != nil {
		return "", fmt.Errorf("load bead summaries: %w", err)
	}

	var nSucceeded, nStopped, nNeverRan int
	totalAttempts := 0
	for _, bs := range beads {
		totalAttempts += bs.Attempts
		switch bs.Status {
		case "succeeded":
			nSucceeded++
		case "full_stopped":
			if bs.Attempts == 0 {
				nNeverRan++
			} else {
				nStopped++
			}
		}
	}
	nEscalated := queryEscalatedCount(ctx, tx, projectID)

	wallTimeS := wallTimeDiffSeconds(proj.CreatedAt, proj.UpdatedAt)

	var b strings.Builder

	// Header
	fmt.Fprintf(&b, "# Project: %s (id: %d)\n\n", proj.Label, projectID)
	fmt.Fprintf(&b, "**Status:** %s  \n", proj.Status)
	fmt.Fprintf(&b, "**Beads:** %d total — %d succeeded, %d escalated, %d full_stopped, %d never ran  \n",
		len(beads), nSucceeded, nEscalated, nStopped, nNeverRan)
	fmt.Fprintf(&b, "**Total attempts:** %d  \n", totalAttempts)
	if wallTimeS > 0 {
		fmt.Fprintf(&b, "**Wall time:** %ds (%dm)  \n", wallTimeS, wallTimeS/60)
	}
	fmt.Fprintf(&b, "**Completed:** %s  \n", proj.UpdatedAt)
	if proj.Status == "full_stopped" {
		b.WriteString("\n> **Note:** Project did not complete. Files from unsucceeded beads may be incomplete or incorrect — see individual bead reports.\n")
	}
	b.WriteString("\n---\n\n")

	// Bead Summary
	b.WriteString("## Bead Summary\n\n")
	b.WriteString("| Bead | Title | Status | Attempts | Revisions | Wall time |\n")
	b.WriteString("|------|-------|--------|----------|-----------|----------|\n")
	for _, bs := range beads {
		fmt.Fprintf(&b, "| %d | %s | %s | %d | %d | %ds |\n",
			bs.BeadID, bs.Title, bs.Status, bs.Attempts, bs.Revisions, bs.WallTimeS)
	}
	b.WriteString("\n")

	// Attempt Distribution (succeeded beads only)
	b.WriteString("## Attempt Distribution\n\n")
	dist := map[int]int{}
	for _, bs := range beads {
		if bs.Status == "succeeded" {
			dist[bs.Attempts]++
		}
	}
	keys := make([]int, 0, len(dist))
	for k := range dist {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	b.WriteString("| Attempts to succeed | Bead count |\n")
	b.WriteString("|---------------------|------------|\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "| %d | %d |\n", k, dist[k])
	}
	b.WriteString("\n")

	// Final Source Files
	b.WriteString("## Final Source Files\n\n")
	b.WriteString("*All files in the project folder at report time, excluding traces/, design_doc.md, go.sum, and .git/.*\n\n")
	renderSourceFiles(&b, folderPath)

	return b.String(), nil
}

func renderSourceFiles(b *strings.Builder, folderPath string) {
	var files []string
	_ = filepath.Walk(folderPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			name := info.Name()
			if name == "traces" || name == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		name := info.Name()
		if name == "design_doc.md" || name == "go.sum" {
			return nil
		}
		rel, _ := filepath.Rel(folderPath, path)
		files = append(files, rel)
		return nil
	})
	sort.Strings(files)

	for _, rel := range files {
		abs := filepath.Join(folderPath, rel)
		content, err := os.ReadFile(abs)
		if err != nil {
			fmt.Fprintf(b, "### %s\n\n*(unreadable: %v)*\n\n", rel, err)
			continue
		}
		lang := codeLanguage(rel)
		fmt.Fprintf(b, "### %s\n\n```%s\n%s\n```\n\n", rel, lang, string(content))
	}
}

// wallTimeDiffSeconds parses two RFC3339 timestamps and returns the elapsed seconds.
func wallTimeDiffSeconds(start, end string) int {
	layouts := []string{
		time.RFC3339,
		"2006-01-02T15:04:05Z",
		"2006-01-02T15:04:05",
	}
	var t0, t1 time.Time
	for _, layout := range layouts {
		if t, err := time.Parse(layout, start); err == nil {
			t0 = t
			break
		}
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, end); err == nil {
			t1 = t
			break
		}
	}
	if t0.IsZero() || t1.IsZero() {
		return 0
	}
	return int(t1.Sub(t0).Seconds())
}
