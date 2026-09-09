package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var nameRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

type SavedExpr struct{ Name, Expr string }

func SaveExpr(name, expr, dir string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("invalid save name %q", name)
	}
	return os.WriteFile(filepath.Join(dir, name+".cron"), []byte(expr), 0o644)
}

func ListSaved(dir string) ([]SavedExpr, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil
	}
	var out []SavedExpr
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".cron") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		out = append(out, SavedExpr{Name: strings.TrimSuffix(e.Name(), ".cron"), Expr: strings.TrimSpace(string(b))})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func checkSaved() {
	dir, _ := os.MkdirTemp("", "cronsaved")
	defer os.RemoveAll(dir)
	fmt.Println("=== saved bead ===")
	err := SaveExpr("weekday-mornings", "0 9 * * 1-5", dir)
	b, _ := os.ReadFile(filepath.Join(dir, "weekday-mornings.cron"))
	fmt.Printf("  SaveExpr ok=%v file=%q\n", err, string(b))
	err = SaveExpr("bad name!", "x", dir)
	_, statErr := os.Stat(filepath.Join(dir, "bad name!.cron"))
	fmt.Printf("  SaveExpr(bad name!) err=%v fileExists=%v\n", err != nil, statErr == nil)
	os.WriteFile(filepath.Join(dir, "b.cron"), []byte("0 0 * * 0\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "a.cron"), []byte("* * * * *"), 0o644)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignore me"), 0o644)
	got, _ := ListSaved(dir)
	fmt.Printf("  ListSaved -> %+v\n", got)
	missing, mErr := ListSaved(filepath.Join(dir, "nope"))
	fmt.Printf("  ListSaved(missing) -> %v %v\n", missing, mErr)
	regFile := filepath.Join(dir, "a.cron")
	rf, rfErr := ListSaved(regFile)
	fmt.Printf("  ListSaved(regular file) -> %v %v\n", rf, rfErr)
}
