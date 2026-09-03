package qualify

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// These mirror the small unexported helpers in internal/execution/bakeoff.go.
// They are 3–5 lines each and duplicated rather than widening the pipeline
// package's API surface for a qualification-only harness.

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func sanitize(s string) string {
	return strings.NewReplacer("/", "_", ":", "_", " ", "_").Replace(s)
}

// unloadModel asks the remote Ollama to drop a model from VRAM. Best-effort.
func unloadModel(baseURL, model string) {
	body := `{"model":"` + model + `","keep_alive":0}`
	_ = exec.Command("curl", "-s", "-m", "20", "-XPOST", baseURL+"/api/generate", "-d", body).Run()
	time.Sleep(2 * time.Second)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func copyTree(src, dst string) error {
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if err := copyFile(p, target); err != nil {
			return err
		}
		return os.Chmod(target, 0o644)
	})
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

func percentile(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	if p <= 0 {
		return s[0]
	}
	if p >= 1 {
		return s[len(s)-1]
	}
	idx := int(p*float64(len(s)-1) + 0.5)
	return s[idx]
}
