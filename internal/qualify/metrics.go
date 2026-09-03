package qualify

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// ModelSummary aggregates every (case × run) replay for one candidate model on
// one verb.
type ModelSummary struct {
	Verb  string
	Model string
	N     int // total replays (cases × runs)

	RunErrorRate     float64
	FirstTryValid    float64
	RubricPassRate   float64 // fraction of gradable runs whose RunGrade.Pass
	PartialGrades    int
	MalformedRate    float64
	RunawayRate      float64
	DeadTurnRate     float64
	AgreementRate    float64 // JUDGE/ADJUDICATE: grade.Cols["agree"]=="yes"; -1 if n/a

	LatencyP50 time.Duration
	LatencyP90 time.Duration
	LatencyMax time.Duration

	GenTokP50  int64
	PromptTok  int64
	TokPerSec  float64
	TurnsP50   float64

	// PerCase is one row per case: the modal verdict / outcome, for the report.
	PerCase []CaseLine
}

type CaseLine struct {
	Case    string
	Pass    int
	Total   int
	Note    string
	Cols    map[string]string
}

// Summarize folds results+grades (index-aligned) into a ModelSummary.
func Summarize(verb, model string, results []ReplayResult, grades []RunGrade) ModelSummary {
	s := ModelSummary{Verb: verb, Model: model, N: len(results), AgreementRate: -1}
	if len(results) == 0 {
		return s
	}

	var lat []float64
	var genTok []float64
	var turns []float64
	var okSizes []float64
	var runErr, firstValid, malformed, dead int
	var gradable, passed, agreeYes, agreeTot int
	var tokNum, tokDen int64
	var promptTokTot int64

	caseAgg := map[string]*CaseLine{}
	var caseOrder []string

	for i, r := range results {
		if r.RunErr != nil {
			runErr++
		} else {
			lat = append(lat, r.Wall.Seconds())
			turns = append(turns, float64(len(r.Calls)))
			genTok = append(genTok, float64(r.genTokens()))
			promptTokTot += r.promptTokens()
			for _, c := range r.Calls {
				tokNum += c.Stats.EvalCount
				tokDen += c.Stats.EvalDuration
			}
			if r.ValidationResult == "valid" {
				firstValid++
				okSizes = append(okSizes, float64(len(r.RawOutput)))
			} else {
				malformed++
			}
			if r.deadTurns() > 0 {
				dead++
			}
		}

		g := grades[i]
		cl := caseAgg[r.Case]
		if cl == nil {
			cl = &CaseLine{Case: r.Case, Cols: g.Cols}
			caseAgg[r.Case] = cl
			caseOrder = append(caseOrder, r.Case)
		}
		cl.Total++
		if g.Note != "" {
			cl.Note = g.Note
		}
		if g.Cols != nil {
			gradable++
			if g.Pass {
				passed++
				cl.Pass++
			}
			if g.Partial {
				s.PartialGrades++
			}
			if v, ok := g.Cols["agree"]; ok {
				agreeTot++
				if v == "yes" {
					agreeYes++
				}
			}
		}
	}

	n := float64(len(results))
	s.RunErrorRate = float64(runErr) / n
	s.FirstTryValid = float64(firstValid) / n
	s.MalformedRate = float64(malformed) / n
	s.DeadTurnRate = float64(dead) / n
	if gradable > 0 {
		s.RubricPassRate = float64(passed) / float64(gradable)
	}
	if agreeTot > 0 {
		s.AgreementRate = float64(agreeYes) / float64(agreeTot)
	}

	medSize := median(okSizes)
	runaway := 0
	for _, sz := range okSizes {
		if medSize > 0 && sz > 4*medSize {
			runaway++
		}
	}
	if len(okSizes) > 0 {
		s.RunawayRate = float64(runaway) / float64(len(okSizes))
	}

	s.LatencyP50 = secs(percentile(lat, 0.5))
	s.LatencyP90 = secs(percentile(lat, 0.9))
	s.LatencyMax = secs(percentile(lat, 1))
	s.GenTokP50 = int64(percentile(genTok, 0.5))
	s.TurnsP50 = median(turns)
	s.PromptTok = promptTokTot / maxi64(int64(len(lat)), 1)
	if tokDen > 0 {
		s.TokPerSec = float64(tokNum) / (float64(tokDen) / 1e9)
	}

	for _, name := range caseOrder {
		s.PerCase = append(s.PerCase, *caseAgg[name])
	}
	sort.Slice(s.PerCase, func(i, j int) bool { return s.PerCase[i].Case < s.PerCase[j].Case })
	return s
}

func secs(f float64) time.Duration { return time.Duration(f * float64(time.Second)) }
func maxi64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// Table renders the cross-model comparison for one verb.
func Table(verb string, sums []ModelSummary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n== %s ==\n", verb)
	fmt.Fprintf(&b, "%-34s %5s %8s %8s %8s %7s %7s %8s %8s %8s %8s\n",
		"MODEL", "N", "lat_p50", "lat_p90", "lat_max", "tok/s", "turns",
		"1st_ok", "rubric", "agree", "dead")
	for _, s := range sums {
		agree := "  -  "
		if s.AgreementRate >= 0 {
			agree = fmt.Sprintf("%4.0f%%", s.AgreementRate*100)
		}
		fmt.Fprintf(&b, "%-34s %5d %7.0fs %7.0fs %7.0fs %7.0f %7.1f %7.0f%% %6.0f%% %5s %7.0f%%\n",
			trunc(s.Model, 34), s.N,
			s.LatencyP50.Seconds(), s.LatencyP90.Seconds(), s.LatencyMax.Seconds(),
			s.TokPerSec, s.TurnsP50,
			s.FirstTryValid*100, s.RubricPassRate*100, agree, s.DeadTurnRate*100)
	}
	// Per-case detail.
	for _, s := range sums {
		fmt.Fprintf(&b, "\n  %s:\n", s.Model)
		for _, cl := range s.PerCase {
			fmt.Fprintf(&b, "    %-10s %d/%d  %s  %s\n", cl.Case, cl.Pass, cl.Total,
				kvString(cl.Cols), trunc(cl.Note, 90))
		}
	}
	return b.String()
}

func kvString(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, k+"="+m[k])
	}
	return strings.Join(parts, " ")
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// TSVHeader / tsvRow: machine-readable dump, one row per ModelSummary.
func TSVHeader() string {
	return strings.Join([]string{
		"verb", "model", "n", "run_err", "first_ok", "rubric_pass", "agree",
		"malformed", "runaway", "dead_turn", "lat_p50_s", "lat_p90_s", "lat_max_s",
		"gen_tok_p50", "prompt_tok", "tok_per_sec", "turns_p50",
	}, "\t")
}

func (s ModelSummary) TSVRow() string {
	agree := ""
	if s.AgreementRate >= 0 {
		agree = fmt.Sprintf("%.3f", s.AgreementRate)
	}
	return strings.Join([]string{
		s.Verb, s.Model, itoa(s.N),
		f3(s.RunErrorRate), f3(s.FirstTryValid), f3(s.RubricPassRate), agree,
		f3(s.MalformedRate), f3(s.RunawayRate), f3(s.DeadTurnRate),
		fmt.Sprintf("%.0f", s.LatencyP50.Seconds()),
		fmt.Sprintf("%.0f", s.LatencyP90.Seconds()),
		fmt.Sprintf("%.0f", s.LatencyMax.Seconds()),
		itoa64(s.GenTokP50), itoa64(s.PromptTok),
		fmt.Sprintf("%.1f", s.TokPerSec), fmt.Sprintf("%.1f", s.TurnsP50),
	}, "\t")
}

func f3(f float64) string    { return fmt.Sprintf("%.3f", f) }
func itoa(i int) string      { return fmt.Sprintf("%d", i) }
func itoa64(i int64) string  { return fmt.Sprintf("%d", i) }
