package main

import (
	"context"
	"flag"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"ratchet/internal/db"
	"ratchet/internal/execution"
	"ratchet/internal/ollama"
	"ratchet/internal/orchestrator"
	"ratchet/internal/project"
	"ratchet/internal/ui"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	// Subcommands are dispatched before flag.Parse so each subcommand can
	// define its own flag set without conflicting with the orchestrator flags.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "new-project":
			project.RunNewProjectMain(os.Args[2:])
			return
		case "start":
			runStart(os.Args[2:])
			return
		case "ui":
			runUI(os.Args[2:])
			return
		case "monitor":
			execution.RunMonitorMain(os.Args[2:])
			return
		case "execute-bead":
			execution.RunExecuteBeadMain(os.Args[2:])
			return
		}
	}

	// Default: run the orchestrator.
	dbPath := flag.String("db", "ratchet.db", "path to the SQLite database")
	ollamaURL := flag.String("ollama", "http://192.168.50.241:11434", "Ollama base URL")
	flag.Parse()

	database, err := db.Open(*dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer database.Close()

	oc := ollama.New(*ollamaURL)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	slog.Info("ratchet starting", "db", *dbPath, "ollama", *ollamaURL)
	if err := orchestrator.Run(ctx, database, oc); err != nil {
		log.Fatalf("orchestrator: %v", err)
	}
}

// runStart opens the DB once and runs both the orchestrator and UI in the
// same process. The DB is fully initialized before either service starts,
// eliminating the startup race that occurs when two separate processes both
// call db.Open on a fresh database simultaneously.
func runStart(args []string) {
	flags := flag.NewFlagSet("start", flag.ExitOnError)
	dbPath := flags.String("db", "ratchet.db", "path to the SQLite database")
	ollamaURL := flags.String("ollama", "http://192.168.50.241:11434", "Ollama base URL")
	addr := flags.String("addr", "localhost:8080", "UI listen address")
	_ = flags.Parse(args)

	database, err := db.Open(*dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer database.Close()

	oc := ollama.New(*ollamaURL)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	slog.Info("ratchet starting", "db", *dbPath, "ollama", *ollamaURL, "addr", *addr)

	uiDone := make(chan error, 1)
	go func() { uiDone <- ui.Run(ctx, database, *addr) }()

	if err := orchestrator.Run(ctx, database, oc); err != nil {
		log.Fatalf("orchestrator: %v", err)
	}
	if err := <-uiDone; err != nil {
		log.Fatalf("ui: %v", err)
	}
}

func runUI(args []string) {
	flags := flag.NewFlagSet("ui", flag.ExitOnError)
	dbPath := flags.String("db", "ratchet.db", "path to the SQLite database")
	addr := flags.String("addr", "localhost:8080", "address to listen on")
	_ = flags.Parse(args)

	database, err := db.Open(*dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer database.Close()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := ui.Run(ctx, database, *addr); err != nil {
		log.Fatalf("ui: %v", err)
	}
}
