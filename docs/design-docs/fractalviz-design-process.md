# FractalViz — From Prose to Cleared Design Doc

A record of how the FractalViz design doc was produced on 2026-09-06: the vague
prose it started from, the initial drafting pass, the changes the independent
review pass proposed, and the final reconciliation. Written as a process artifact
for the ratchet from-scratch baseline run — this was the first end-to-end
exercise of the `draft-design-doc` → `check-design-doc` skill chain on input the
framework had never seen.

---

## 1. The starting point

### The original ask

Four bullets, no detail:

> Web application to visualize fractals:
> - see the major fractals, 3 or 4 types
> - adjust the values that go into creating the fractal, with a standard set of
>   default values
> - save a particular image if I like it
> - written in Go with HTMX

### The framing that came with the task

The planning conversation had already narrowed the shape, to keep the
decomposition clean and steer clear of known tar pits:

- **One escape-time core, four variations** — Mandelbrot, Julia, Burning Ship,
  Multibrot (`z^d + c`). **No Newton fractals** (basin-of-attraction / root-finding
  math is an implicit-domain-knowledge trap).
- **HTMX means "handlers return HTML fragments"** — `html/template` partials,
  tests assert fragment content and the image endpoint's `Content-Type`, not
  browser swap behavior.
- **Server-side PNG via stdlib only** (`math/cmplx`, `image`, `image/png`) —
  mechanically testable via reference pixel values and PNG dimensions.
- Two decisions left as brackets for the drafter to fill or raise: **image size**
  (suggested 600×600) and **how "save" works** (write to a `saved/` dir and list
  it, vs. browser download).

Everything below — the exact formulas, the parameter defaults, the
complex-plane→pixel mapping, the palette, the file layout, the bead boundaries —
was undetermined at this point.

---

## 2. Initial design (`draft-design-doc`)

### Inputs read

`docs/design_doc_guide.md` and `docs/design_doc_template.md` in full, plus one
existing worked design doc (`exprvm-web`) as a reference for house style —
strict file-ownership rules, `var _` export-signature assertions, pinned worked
values, cross-bead contracts.

### Structural choices

| Decision | Choice |
|---|---|
| Core abstraction | One `Escape(p Params, point complex128) int` function; the four types differ only in `z0`, `c`, and the update function |
| Source files | 7: `fractal.go`, `render.go`, `params.go`, `save.go`, `handlers.go`, `templates.go`, `main.go` — one flat `package main` |
| Beads | 8: fractal-core · render · params · save · templates · handlers · main · integration |
| Image size | 600×600, compile-time constant, never request-controlled |
| Save | Server-side `saved/` dir; filenames `fmt.Sprintf("%019d-%s.png", now.UnixNano(), type)`; gallery sorted newest-first by filename |
| Routes | `GET /` · `POST /render` · `GET /fractal.png` · `POST /save` · `GET /saved/` |

### Load-bearing math decisions

Each written against "what is the first plausible wrong implementation?", per the
guide's small-model discipline:

- **Escape test evaluated *before* each update**, strict `>` on the *squared*
  magnitude vs. the *squared* radius. Natural error: update `z` first, then test —
  returns `n+1` where this returns `n`.
- **Julia uses `z0 = point`, `c = (JuliaRe, JuliaIm)`.** Natural error:
  implement it like Mandelbrot (`z0 = 0`, `c = point`) — produces the Mandelbrot
  set for every parameter.
- **Multibrot raises `z` to an integer power by repeated multiplication**, not
  `cmplx.Pow` (which is not exact for integer exponents and would disagree with
  the pinned test values).
- **Burning Ship abs's the components of `z` before squaring**, not the result of
  `z*z`.
- **`PixelToComplex`**: window height in complex units is `4.0 / Zoom`; sample the
  pixel *centre* (`+ 0.5`); imaginary part *decreases* as the pixel row increases
  (row 0 is the top). Natural errors: omit the half-pixel offset, or flip the
  imaginary sign (invisible on the near-symmetric Mandelbrot set, wrong for the
  others).
- **`Color`**: `n >= maxIter` → opaque black; otherwise a blue→white ramp
  `RGBA{v, v, 255, 255}`, `v = uint8(255*n/maxIter)` (integer division). `n = 0`
  is pure blue, *not* black — so an immediately-escaping pixel is distinguishable
  from an in-set pixel.

### Script verification

Every worked value in the doc was computed by a throwaway Go program in-session
before it went in, per the skill's hard rule. Three programs:

1. **Escape counts** for all four types — e.g. Mandelbrot `1+0i` → 3 (orbit
   `0 → 1 → 2 → 5`), Julia rabbit `0+0i` → 100 (in-set), Burning Ship `1+1i` → 2,
   Multibrot d=3 `1.5+0i` → 2. Also used to *find* an in-set point for each type
   (needed for the "interior is black" render test).
2. **`PixelToComplex`** corners for 600×600, centre `(-0.5, 0)`, zoom 1 — e.g.
   pixel `(0,0)` → `(-2.4966…, +1.9966…)`, pixel `(300,300)` → `(-0.4966…, -0.0033…)`.
3. **PNG round-trip, `ImageQuery` encoding, and filename sort** — confirmed a
   600×600 `image.RGBA` encodes and decodes with bounds preserved and pixel
   colours intact, and that zero-padded nanosecond filenames sort chronologically
   under reverse-lexical order.

### Open Questions raised

Five, all surfaced rather than silently filled: escaped-pixel palette; Julia
default constant; save mechanism; per-type form fields; gallery scope.

---

## 3. Interactive resolution

| # | Question | Resolution | Effect on the design |
|---|---|---|---|
| 1 | Escaped-pixel palette | Blue→white ramp, in-set black | none — kept as drafted |
| 2 | Julia default `c` | Douady rabbit `-0.123 + 0.745i` (has interior) | none — kept as drafted |
| 3 | How "save" works | Server-side `saved/` dir + gallery at `GET /saved/` | none — kept as drafted |
| 4 | Per-type form fields | **Show / hide per selected type** | **changed** — added `POST /select` → `HandleSelect`, `ShowJulia` / `ShowExponent` bools on `PageView`, `{{if}}` blocks in the form template |
| 5 | Gallery scope (click-to-reload params?) | Deferred — left in `## Open Questions` | none — 1 Open Question remains |

Item 4 was the only answer that moved the design. The draft had proposed always
showing all eight numeric inputs; the choice to show only the relevant ones per
type required a dedicated handler that resets the form to the new type's defaults
on a `<select>` change.

---

## 4. My own pre-review pass

While the independent review was being set up, a non-independent re-read of the
draft caught three internal inconsistencies, all in the saved-file naming — and
all fixed before the review returned:

- **`%019d` padding.** `time.Now().UnixNano()` for any date since 2023 is already
  exactly 19 digits, so `%019d` adds no leading zeros. The pinned example filename
  had been written with a typo'd `000…` prefix (and an extra digit). Corrected and
  re-verified with a script.
- **`.png` in the return value.** One section defined the filename stem without
  the extension and appended `.png` to the path; another had the function return
  the name *with* `.png`. Unified — `.png` is part of the returned name.
- **`ListSaved` on a missing directory.** "Returns an empty slice" in one place,
  "returns `(nil, nil)`" in another. Unified to `(nil, nil)`.

---

## 5. Independent ambiguity review

### Setup

A fresh subagent with **only** the design doc and
`docs/design_doc_ambiguity_checklist.md` — no memory of the drafting
conversation, no knowledge of who wrote the doc or why, no sight of the
mechanical scan's output. This independence is the mechanism: a same-context
reviewer shares the author's blind spots.

The mechanical pre-filter (`cmd/checkdesigndoc`, covering ambiguity classes
1/2/6/7/17 plus pin-vs-scenario counts) had already run: one class-1 hit (a
false positive on the word "forward" in a temporal phrase), pin counts
consistent.

### The subagent's three findings

The subagent had snapshotted the doc at launch — *before* the §4 fixes landed —
so two of its three findings independently re-derived bugs already fixed, which
is useful confirmation that they were real and worth fixing:

1. **Filename format vs. pinned literal (class 13 / 2).** `%019d` of a 19-digit
   input produces no padding, but the pinned example asserted a `000…`-padded
   string — the format string and its own worked example computed different
   filenames. *Already fixed in §4.*
2. **Return value: `.png` or not (class 13).** The signature-block comment and
   the behavioral spec specified different return values. *Already fixed in §4.*
3. **`assemble(p)` never states `PageView.Params = p` (class 11).** *New — missed
   by both the mechanical scan and the §4 self-review.* The `assemble(p)` helper
   was written as a field-by-field list that assigned five of `PageView`'s six
   fields and silently omitted the load-bearing one. A literal reader
   implementing exactly the listed bullets leaves `Params` at its zero value —
   every form input then renders `0`, and the "submitted values become the new
   defaults" round-trip breaks. None of the handler-bead exit criteria would
   catch it, because `Types` and the `ShowJulia`/`ShowExponent` flags are
   computed from `p` directly and still work.

### What the review cleared

The subagent traced every pinned worked value against the formula it derives from
and confirmed agreement: the escape-time math for all four types, `Color`'s
integer-division ramp, `PixelToComplex`'s sign convention and corner
coordinates, the `ImageQuery` round-trip. It also confirmed clean results for
copy/ownership semantics (class 3), HTMX fragment scope (class 4), protocol
completeness (class 5), sentinel overloading (class 15), and classes
6/7/10/12/14/16/17.

Notably, the ambiguity checklist's class-13 entry cites a *historical* "fractal"
incident — a worked test example whose coordinates disagreed with the
pixel-mapping formula they were derived from. The subagent explicitly confirmed
that trap is **absent** here: because the `PixelToComplex` pins were
script-derived from the exact formula in the doc, the two agree.

---

## 6. Reconciliation and sign-off

| # | Class | Item | Source | Resolution |
|---|---|---|---|---|
| 1 | 11 | `assemble(p)` omits `Params = p` | judgment (subagent) | **Applied** — added `Params = p` as the first `assemble(p)` bullet, with the "renders as form defaults; zero-value → all-zeros" rationale, plus a matching `PageView.Params` struct comment |
| 2 | 1 | line-505 "forward" hit | mechanical, unresolved | **Waived** — recorded as an HTML-comment waiver at the spot; the phrase is temporal, not a directional claim |
| 3 | 13 / 2 | `SaveImage` filename width | subagent + self-review | **`%019d` kept** — sufficient (int64 nanos max at 19 digits; every real timestamp 1678–2262 AD is exactly 19 chars, so lexical order already equals chronological). Subagent had suggested `%022d`; declined as cosmetic |
| 4 | 13 | `SaveImage` return includes `.png` | subagent + self-review | **Confirmed** — fixed in §4, `.png` carried in the return value and `SavedImage.Filename` throughout |
| 5 | — | `PixelToComplex` general vs. square-image | judgment (mine, labelled) | **Left as-is** — handlers always pass the `600` constants; no live divergence |

Final `checkdesigndoc`: one class-1 hit (the waived line-505 false positive),
pin/scenario counts consistent. All 17 ambiguity classes addressed.

**Result: cleared for `new-project`.**

---

## 7. What this exercised

- **The bootstrap FSM on fresh input.** Every ratchet clone since baseline-7
  entered at a revived tail bead, so `SURVEY → VERIFY_MANIFEST → CERTIFY →
  DECOMPOSE → AUDIT` had run on genuinely new input maybe twice in two months.
  This design doc is the entry point for the first real from-scratch run since.
- **The `draft-design-doc` → `check-design-doc` skill chain end to end**, itself
  previously unvalidated.
- **The value of the independent pass, concretely.** It independently re-derived
  two real bugs (confirming they were worth the fix) and caught a third — a
  silently-incomplete constructor spec — that neither the mechanical scan nor a
  non-independent re-read of the same document had found.
