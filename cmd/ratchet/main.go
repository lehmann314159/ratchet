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
