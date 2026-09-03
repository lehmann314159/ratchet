package qualify

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// runScaffoldMutants extracts the known-good implementation files for every
// REFINE_TESTS_WRITE bead in the corpus from the baseline artifact tarball into
// <out>/b<bead>/good/, and drops a README naming the impl files a human should
// mutate. Mutant authoring itself stays manual (2–3 per bead).
func runScaffoldMutants(args []string) {
	fs := flag.NewFlagSet("qualify-model scaffold-mutants", flag.ExitOnError)
	corpus := fs.String("corpus", "", "capture-verb-io output dir (required)")
	artifact := fs.String("artifact", "", "baseline artifact .tar.gz with the final impl files (required)")
	outDir := fs.String("out", "internal/qualify/testdata/qual-mutants", "mutant-fixtures root")
	_ = fs.Parse(args)

	if *corpus == "" || *artifact == "" {
		slog.Error("scaffold-mutants: --corpus and --artifact are required")
		os.Exit(1)
	}

	corp, err := LoadCorpus(*corpus)
	if err != nil {
		slog.Error("scaffold-mutants: load corpus", "error", err)
		os.Exit(1)
	}

	tb, err := readTarGz(*artifact)
	if err != nil {
		slog.Error("scaffold-mutants: read artifact", "error", err)
		os.Exit(1)
	}

	ctx := context.Background()
	seen := map[int64]bool{}
	for _, c := range corp.Cases {
		if c.Meta.Verb != "REFINE_TESTS_WRITE" || c.Meta.BeadID == nil || seen[*c.Meta.BeadID] {
			continue
		}
		beadID := *c.Meta.BeadID
		seen[beadID] = true

		spec, err := caseBeadSpec(ctx, filepath.Join(c.Dir, "db.sqlite"), beadID)
		if err != nil {
			slog.Warn("scaffold-mutants: bead spec", "bead", beadID, "error", err)
			continue
		}
		impls := spec.ImplFiles()
		if len(impls) == 0 {
			slog.Info("scaffold-mutants: skip (no impl files)", "bead", beadID, "title", spec.Title)
			continue
		}
		goodDir := filepath.Join(*outDir, fmt.Sprintf("b%d", beadID), "good")
		if err := os.MkdirAll(goodDir, 0o755); err != nil {
			slog.Error("scaffold-mutants: mkdir", "error", err)
			os.Exit(1)
		}
		var wrote []string
		for _, rel := range impls {
			base := path.Base(rel)
			data, ok := tb[base]
			if !ok {
				slog.Warn("scaffold-mutants: impl file not in artifact", "bead", beadID, "file", base)
				continue
			}
			if err := os.WriteFile(filepath.Join(goodDir, base), data, 0o644); err != nil {
				slog.Error("scaffold-mutants: write", "error", err)
				os.Exit(1)
			}
			wrote = append(wrote, base)
		}
		readme := fmt.Sprintf(`# bead %d — %s

impl files: %s
required test funcs: %s

good/ holds the baseline-8 final implementation (test must PASS against it).

Create 2–3 sibling dirs m1_<desc>/, m2_<desc>/, ... each containing a full copy
of the impl file(s) above with ONE realistic defect injected (off-by-one, wrong
operator, dropped nil-check, wrong boundary). The WRITE grader scores a run as
passing only if the generated test compiles, passes good/, and fails ≥1 mutant.
`, beadID, spec.Title, strings.Join(wrote, ", "), strings.Join(spec.RequiredTestFuncs(), ", "))
		_ = os.WriteFile(filepath.Join(*outDir, fmt.Sprintf("b%d", beadID), "README.md"), []byte(readme), 0o644)
		slog.Info("scaffold-mutants: wrote good/", "bead", beadID, "files", wrote)
	}
	fmt.Printf("scaffolded into %s — now hand-author m*/ mutant dirs\n", *outDir)
}

func readTarGz(p string) (map[string][]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	out := map[string][]byte{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			return nil, err
		}
		out[path.Base(h.Name)] = data
	}
	return out, nil
}
