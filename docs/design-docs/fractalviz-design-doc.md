# FractalViz — Design Document

## Overview

A single-user web application for exploring escape-time fractals. The server renders
fractal images on demand as PNGs using only the Go standard library
(`math`, `math/cmplx`, `image`, `image/png`), and serves an HTML UI updated with HTMX
fragment swaps (`html/template` partials returned from handlers — no client-side
JavaScript beyond the HTMX library itself, no browser-swap behavior asserted in tests).
The user picks one of four fractal types, adjusts numeric parameters (plane center, zoom,
iteration cap, escape radius, and the per-type parameters), sees the rendered image, and
can save an image they like to a server-side directory whose contents are shown as a
gallery on the page.

All four fractal types are variations on **one escape-time iteration core** — for each
pixel, map it to a complex-plane point, iterate a per-type update function, and count how
many iterations pass before the orbit leaves a disc of radius `EscapeR` (or reach the
iteration cap). The four types differ only in the starting value `z0`, the constant `c`,
and the update function:

| Type | `z0` | `c` | update |
|---|---|---|---|
| Mandelbrot | `0` | pixel point | `z → z² + c` |
| Julia | pixel point | `(JuliaRe, JuliaIm)` | `z → z² + c` |
| Burning Ship | `0` | pixel point | `z → (\|Re z\| + i·\|Im z\|)² + c` |
| Multibrot | `0` | pixel point | `z → z^Exponent + c` (integer power) |

**Runtime model:** one HTTP server process (`package main`, listens on `:8080`). One
shared, in-memory set of defaults; saved images persist as files in a `saved/`
directory relative to the working directory. There are no user accounts, no sessions, and
no database.

**Out of scope** (do not implement):
- Newton fractals or any non-escape-time fractal; only the four types above.
- Smooth/continuous escape colouring, histogram colouring, anti-aliasing/supersampling,
  or any per-pixel technique beyond one integer escape count per pixel.
- Pan/zoom by clicking the image, drag-to-select a region, or animation. The user changes
  the view only by editing the numeric form fields.
- Re-loading a saved image's parameters back into the form. The gallery is view-only.
- User-adjustable image dimensions. The rendered image is always 600×600 pixels.
- Persistence of anything other than saved PNG files (no saved-parameter sidecars, no
  history of past renders).
- Concurrency beyond a single mutex guarding writes to the `saved/` directory; this is a
  one-operator tool.

**Domain parameters** (stated so nothing is guessed):
- **Image size:** always 600×600 pixels. `ImageWidth = ImageHeight = 600`, compile-time
  constants, never read from a request.
- **Complex-plane ↔ pixel mapping.** The image shows a rectangular window of the complex
  plane centred on `(CenterRe, CenterIm)`. The window's **height in complex units is
  `4.0 / Zoom`**; its width is `height × ImageWidth / ImageHeight` (so width == height for
  the square image). Pixel `(px, py)` with `px ∈ [0, width)`, `py ∈ [0, height)` maps to
  the complex-plane coordinate of that **pixel's centre** (the `+ 0.5` terms below). `px`
  increases rightward and the real part increases with it; `py` increases **downward** and
  the imaginary part **decreases** with it (pixel row 0 is the top of the window = the
  largest imaginary value). Exact formula in `PixelToComplex` below.
- **Escape test.** The orbit has "escaped" at the first iteration where
  `real(z)*real(z) + imag(z)*imag(z) > EscapeR*EscapeR` — a strict `>` on the **squared**
  magnitude against the **squared** radius (no `math.Sqrt`, no `math.Hypot`). The test is
  evaluated **before** each application of the update function (so a point whose `z0`
  already lies outside the disc escapes at iteration 0).
- **Escape count.** `Escape` returns the iteration index `n` at which the escape test
  first passes — equivalently, the number of times the update function was applied before
  the orbit left the disc. If the test never passes within `MaxIter` iterations, `Escape`
  returns `MaxIter` exactly, and the point is considered **in the set**.
- **Multibrot power is an integer power computed by repeated multiplication**:
  `z^d = z * z * … * z` (`d` factors), *not* `cmplx.Pow` (which introduces
  floating-point error and is not exact even for integer exponents).
- **Number formats.** `CenterRe`, `CenterIm`, `Zoom`, `EscapeR`, `JuliaRe`, `JuliaIm` are
  `float64`. `MaxIter` and `Exponent` are `int`. Parameter ranges and clamping are defined
  in `ParseParams` below.
- **Colour.** One `color.RGBA` per pixel, fully opaque (`A = 255`). In-set pixels
  (`n >= MaxIter`) are black. Escaped pixels use a blue→white ramp keyed on `n` — exact
  formula in `Color` below.
- **Saved images.** `SaveImage` renders the image and writes it to `filepath.Join(dir, name)`
  where `name` (returned to the caller, and stored as `SavedImage.Filename`) is
  `fmt.Sprintf("%019d-%s.png", now.UnixNano(), TypeName(p.Type))` — a nanosecond timestamp
  zero-padded to at least 19 digits, a hyphen, the type name, and the `.png` extension.
  The `.png` is part of `name` (not appended separately). Zero-padding makes lexical
  filename order match chronological order. `ListSaved` returns every `*.png` file in the
  directory, **newest first** (reverse lexical filename order).

## Architecture

```
fractalviz/
├── go.mod                — module fractalviz, Go 1.22
├── main.go               — var templates *template.Template; func main() only
├── fractal.go            — FractalType + constants, Params, TypeName, ParseType,
│                            DefaultParams, Escape
├── render.go             — ImageWidth, ImageHeight constants, PixelToComplex, Color, Render
├── params.go             — ParseParams, ImageQuery
├── save.go               — SavedImage, SaveImage, ListSaved, SavedDir constant, saveMu
├── handlers.go           — TypeOption, PageView, HandleIndex, HandleRender,
│                            HandleSelect, HandleImage, HandleSave
├── templates.go          — InitTemplates, RenderPage, RenderResult
└── *_test.go             — one test file per source file above, plus integration_test.go
```

All `.go` files use `package main` at the project root — a single flat package, no
subdirectories. `go.mod` and `do_not_use_this_test.go` are generated automatically by the
scaffolding step — do not list them as SURVEY outputs.

**File assignment rules (strict):**
- `main.go` contains exactly: `var templates *template.Template` and `func main()`.
  Nothing else — no types, no handlers, no constants.
- `fractal.go` contains: `FractalType` and its four constants, `Params`, `TypeName`,
  `ParseType`, `DefaultParams`, `Escape`. No `image`/`color` references, no HTTP.
- `render.go` contains: `ImageWidth`, `ImageHeight`, `PixelToComplex`, `Color`, `Render`.
  It imports `image` and `image/color`. It does **not** contain `Escape` (that lives in
  `fractal.go`) — `Render` calls `Escape`.
- `params.go` contains: `ParseParams`, `ImageQuery`. It imports `net/url` and `strconv`.
  No HTTP handler functions.
- `save.go` contains: `SavedImage`, `SaveImage`, `ListSaved`, `const SavedDir = "saved"`,
  `var saveMu sync.Mutex`. `SaveImage` calls `Render` and `png.Encode`.
- `handlers.go` contains: `TypeOption`, `PageView`, `HandleIndex`, `HandleRender`,
  `HandleSelect`, `HandleImage`, `HandleSave`. No template parsing, no `Escape`/`Render`
  re-implementation.
- `templates.go` contains: `InitTemplates`, `RenderPage`, `RenderResult`. No handler
  functions, no type declarations.
- Do NOT put `HandleIndex`, `HandleRender`, `HandleSelect`, `HandleImage`, or
  `HandleSave` in `templates.go` or `params.go`.
- Do NOT put `PageView` or `TypeOption` in `templates.go` — they belong in `handlers.go`.
- Do NOT put `Escape` in `render.go` or `Render` in `fractal.go`.
- Do NOT put `var templates` anywhere except `main.go`.

## Data Types and Function Signatures

All `.go` source files use `package main`. Module name is `fractalviz`. Requires Go 1.22.

```go
// ---- fractal.go ----

type FractalType int

const (
    Mandelbrot  FractalType = iota // z0 = 0;          z -> z^2 + c,  c = pixel point
    Julia                          // z0 = pixel point; z -> z^2 + c,  c = (JuliaRe, JuliaIm)
    BurningShip                    // z0 = 0;          z -> (|Re z| + i|Im z|)^2 + c,  c = pixel point
    Multibrot                      // z0 = 0;          z -> z^Exponent + c,  c = pixel point
)

type Params struct {
    Type     FractalType
    CenterRe float64 // real part of the complex point at the image centre
    CenterIm float64 // imaginary part of the complex point at the image centre
    Zoom     float64 // window height in complex units = 4.0 / Zoom; clamped to [0.1, 1e12]
    MaxIter  int     // iteration cap; clamped to [1, 1000]
    EscapeR  float64 // escape radius; escape when |z| > EscapeR; clamped to [2.0, 10.0]
    JuliaRe  float64 // Julia constant, real part      (used only when Type == Julia)
    JuliaIm  float64 // Julia constant, imaginary part (used only when Type == Julia)
    Exponent int     // exponent d in z^d + c (used only when Type == Multibrot); clamped to [2, 8]
}

// TypeName returns the lowercase URL/identifier form of t:
// Mandelbrot->"mandelbrot", Julia->"julia", BurningShip->"burningship",
// Multibrot->"multibrot". Panics on an out-of-range FractalType.
func TypeName(t FractalType) string

// ParseType is the inverse of TypeName. Unknown strings return a non-nil error
// and Mandelbrot (the zero value).
func ParseType(s string) (FractalType, error)

// DefaultParams returns the full default Params for a type — every field populated,
// including the per-type fields that type ignores (so the value is always valid to
// serialise). See "DefaultParams" in the Behavioral Specification for the exact values.
func DefaultParams(t FractalType) Params

// Escape runs the escape-time iteration for the pixel whose complex-plane coordinate
// is point. Returns the iteration index at which the (strict, squared) escape test
// first passes, or p.MaxIter if it never does within p.MaxIter iterations.
func Escape(p Params, point complex128) int

// ---- render.go ----

const ImageWidth = 600
const ImageHeight = 600

// PixelToComplex maps pixel (px, py) in a width×height image to its pixel-centre
// complex-plane coordinate under p.CenterRe/CenterIm/Zoom. See Behavioral Spec.
func PixelToComplex(p Params, px, py, width, height int) complex128

// Color maps an escape count to a pixel colour. n >= maxIter (in-set) -> opaque black.
// Otherwise an opaque blue->white ramp: RGBA{v, v, 255, 255} with v = uint8(255*n/maxIter).
func Color(n, maxIter int) color.RGBA

// Render produces the fractal image for p at width×height pixels: for every pixel,
// PixelToComplex -> Escape -> Color -> Set. Returns a fully-populated *image.RGBA
// with bounds (0,0)-(width,height).
func Render(p Params, width, height int) *image.RGBA

// ---- params.go ----

// ParseParams reads a Params from url.Values (works for both r.URL.Query() and
// r.PostForm). Field keys: type, centerRe, centerIm, zoom, maxIter, escapeR,
// juliaRe, juliaIm, exponent. See Behavioral Spec for missing-field, parse-error,
// and clamping rules.
func ParseParams(v url.Values) (Params, error)

// ImageQuery returns the image endpoint path plus an encoded query string that
// ParseParams round-trips exactly: "/fractal.png?" + url.Values{...}.Encode().
func ImageQuery(p Params) string

// ---- save.go ----

const SavedDir = "saved"

var saveMu sync.Mutex

type SavedImage struct {
    Filename string // base name within SavedDir, e.g. "1700000000123456789-mandelbrot.png"
    Title    string // display label; equal to Filename in this version
}

// SaveImage renders p at ImageWidth×ImageHeight and writes it as a PNG to
// filepath.Join(dir, name) where name = fmt.Sprintf("%019d-%s.png", now.UnixNano(), TypeName(p.Type)).
// Returns name (the base filename, .png included) on success. Holds saveMu for the write.
// Returns ("", err) if the directory is not writable or encoding fails.
func SaveImage(p Params, dir string, now time.Time) (string, error)

// ListSaved returns every *.png entry in dir as a SavedImage, sorted newest first
// (reverse lexical order by Filename). A missing directory returns (nil, nil).
func ListSaved(dir string) ([]SavedImage, error)

// ---- handlers.go ----

type TypeOption struct {
    Value    string // TypeName form, e.g. "mandelbrot"
    Label    string // human label, e.g. "Mandelbrot"
    Selected bool   // true when this option is PageView.Params.Type
}

type PageView struct {
    Params       Params       // the params this view renders; every form input's value comes from here (assemble sets Params = p)
    ImageURL     string       // ImageQuery(Params) — the src of the <img> tag
    Types        []TypeOption // one per FractalType in constant order; Selected set for Params.Type
    Saved        []SavedImage // ListSaved(SavedDir) output — newest first
    ShowJulia    bool         // true iff Params.Type == Julia — render the juliaRe/juliaIm inputs
    ShowExponent bool         // true iff Params.Type == Multibrot — render the exponent input
}

func HandleIndex(w http.ResponseWriter, r *http.Request)  // GET /
func HandleRender(w http.ResponseWriter, r *http.Request) // POST /render
func HandleImage(w http.ResponseWriter, r *http.Request)  // GET /fractal.png
func HandleSave(w http.ResponseWriter, r *http.Request)   // POST /save
func HandleSelect(w http.ResponseWriter, r *http.Request) // POST /select

// ---- templates.go ----

func InitTemplates() *template.Template

// RenderPage executes the "page" template (full HTML document) with v, writing to w.
func RenderPage(w http.ResponseWriter, v PageView)

// RenderResult executes the "result" template (the #app fragment only) with v,
// writing to w. This is the body of every POST /render and POST /save response.
func RenderResult(w http.ResponseWriter, v PageView)

// ---- main.go ----

var templates *template.Template
```

### Export signatures

```go
var _ FractalType = Mandelbrot
var _ func(FractalType) string = TypeName
var _ func(string) (FractalType, error) = ParseType
var _ func(FractalType) Params = DefaultParams
var _ func(Params, complex128) int = Escape
var _ func(Params, int, int, int, int) complex128 = PixelToComplex
var _ func(int, int) color.RGBA = Color
var _ func(Params, int, int) *image.RGBA = Render
var _ func(url.Values) (Params, error) = ParseParams
var _ func(Params) string = ImageQuery
var _ func(Params, string, time.Time) (string, error) = SaveImage
var _ func(string) ([]SavedImage, error) = ListSaved
var _ func(http.ResponseWriter, *http.Request) = HandleIndex
var _ func(http.ResponseWriter, *http.Request) = HandleRender
var _ func(http.ResponseWriter, *http.Request) = HandleImage
var _ func(http.ResponseWriter, *http.Request) = HandleSave
var _ func(http.ResponseWriter, *http.Request) = HandleSelect
var _ func() *template.Template = InitTemplates
var _ func(http.ResponseWriter, PageView) = RenderPage
var _ func(http.ResponseWriter, PageView) = RenderResult
var _ *template.Template = templates
var _ int = ImageWidth
var _ int = ImageHeight
var _ string = SavedDir
```

## Behavioral Specification

### `Escape(p Params, point complex128) int`

The single escape-time core. Exact algorithm:

```
// choose z0 and c
if p.Type == Julia:
    z = point
    c = complex(p.JuliaRe, p.JuliaIm)
else:                       // Mandelbrot, BurningShip, Multibrot
    z = complex(0, 0)
    c = point

r2 = p.EscapeR * p.EscapeR

for n := 0; n < p.MaxIter; n++ {
    if real(z)*real(z) + imag(z)*imag(z) > r2 {   // strict >, squared, evaluated BEFORE the update
        return n
    }
    switch p.Type {
    case Mandelbrot, Julia:
        z = z*z + c
    case BurningShip:
        z = complex(math.Abs(real(z)), math.Abs(imag(z)))   // abs the components FIRST
        z = z*z + c                                          // then square, then add c
    case Multibrot:
        // integer power by repeated multiplication — NOT cmplx.Pow
        zp = complex(1, 0)
        for k := 0; k < p.Exponent; k++ { zp = zp * z }
        z = zp + c
    }
}
return p.MaxIter
```

Load-bearing points a natural implementation gets wrong:

- **The escape test is evaluated at the top of the loop, before the update.** The most
  natural wrong version updates `z` first and then tests, returning `n+1` where this
  returns `n`. For Mandelbrot `point = 1+0i`, `MaxIter = 100`, `EscapeR = 2`: the orbit is
  `z0 = 0` (n=0, `0 ≤ 4`), `z1 = 1` (n=1, `1 ≤ 4`), `z2 = 2` (n=2, `4 ≤ 4` — **not**
  `> 4`, strict), `z3 = 5` (n=3, `25 > 4` → **return 3**). Do NOT return 2 or 4.
- **Strict `>` on squared magnitude vs squared radius.** `>=`, or comparing `math.Hypot`
  to `EscapeR`, changes boundary results. Use `real(z)*real(z) + imag(z)*imag(z) > EscapeR*EscapeR`.
- **Julia uses `z0 = point` and a fixed `c`.** Implementing Julia like Mandelbrot
  (`z0 = 0`, `c = point`) is the natural mistake; it produces the Mandelbrot set, not the
  Julia set, for every parameter.
- **Burning Ship abs's the real and imaginary parts of `z` *before* squaring**, not the
  result of `z*z`, and not `c`.
- **Multibrot raises `z` to `p.Exponent` by repeated multiplication.** Hard-coding `z*z*z`
  ignores the parameter; `cmplx.Pow(z, complex(float64(p.Exponent), 0))` is not exact and
  will disagree with the pinned test values.
- **In-set return value is exactly `p.MaxIter`.** Callers use `n >= MaxIter` (equivalently
  `n == MaxIter`) as the in-set test.

<!-- verified: go run scratchpad/verify  =>  Mandelbrot Escape: 0+0i=>100, -1+0i=>100, 2+2i=>1, 1+0i=>3;
     Julia c=-0.123+0.745i: 0+0i=>100, 10+10i=>0, 2+0i=>1;
     BurningShip: 0+0i=>100, 2+2i=>1, 1+1i=>2, -0.5-0.5i=>100;
     Multibrot d=3: 0+0i=>100, 1+0i=>3, 1.5+0i=>2 -->

### `PixelToComplex(p Params, px, py, width, height int) complex128`

```
viewH := 4.0 / p.Zoom
viewW := viewH * float64(width) / float64(height)
re := p.CenterRe + (float64(px) + 0.5 - float64(width)/2)  / float64(width)  * viewW
im := p.CenterIm - (float64(py) + 0.5 - float64(height)/2) / float64(height) * viewH
return complex(re, im)
```

- The `+ 0.5` samples the **centre** of each pixel. Omitting it shifts the whole image
  by half a pixel and changes the pinned corner values.
- The imaginary term is **subtracted**: `py = 0` (top row) maps to the **largest**
  imaginary value (`CenterIm + viewH/2 - half-pixel`), `py = height-1` (bottom row) to the
  smallest. Getting this sign wrong flips the image vertically — invisible on the
  vertically-near-symmetric Mandelbrot set but wrong for Julia/Burning Ship.
- No rounding, no clamping — returns the exact `float64` result.

<!-- verified: go run scratchpad/verify  =>  600x600, center (-0.5,0), zoom 1:
     pixel (0,0)     => (-2.4966666666666666 + 1.9966666666666666i)
     pixel (300,300) => (-0.49666666666666665 - 0.0033333333333333335i)
     pixel (599,599) => (1.4966666666666666  - 1.9966666666666666i) -->

### `Color(n, maxIter int) color.RGBA`

```
if n >= maxIter {
    return color.RGBA{R: 0, G: 0, B: 0, A: 255}   // in-set: opaque black
}
v := uint8(255 * n / maxIter)                      // integer division
return color.RGBA{R: v, G: v, B: 255, A: 255}      // opaque blue -> white ramp
```

- `255 * n / maxIter` is **integer** arithmetic (`n` and `maxIter` are `int`); evaluate
  `255 * n` first, then divide.
- `n = 0` (escaped immediately) is **not** black — it is `{0, 0, 255, 255}` (pure blue).
  Black is reserved for `n >= maxIter`. A version that returns black for `n = 0` makes the
  in-set region indistinguishable from the fastest-escaping pixels.
- `A` is always 255.

<!-- verified: go run scratchpad/verify  =>  Color(0,100)={0,0,255,255}, Color(1,100)={2,2,255,255},
     Color(50,100)={127,127,255,255}, Color(99,100)={252,252,255,255}, Color(100,100)={0,0,0,255},
     Color(33,256)={32,32,255,255} -->

### `Render(p Params, width, height int) *image.RGBA`

Allocates `image.NewRGBA(image.Rect(0, 0, width, height))`, then for every `py` in
`[0, height)` and every `px` in `[0, width)`:
`img.Set(px, py, Color(Escape(p, PixelToComplex(p, px, py, width, height)), p.MaxIter))`.
Returns the image. Pure — no globals, no I/O.

### `TypeName` / `ParseType` / `DefaultParams`

**`TypeName(t)`** — fixed lookup: `Mandelbrot→"mandelbrot"`, `Julia→"julia"`,
`BurningShip→"burningship"`, `Multibrot→"multibrot"`. Any other value: `panic`.

**`ParseType(s)`** — exact inverse; matches the four strings above only (case-sensitive).
Anything else: returns `(Mandelbrot, error)` with a non-nil error.

**`DefaultParams(t)`** — returns this struct, with `Type` set to `t`:

| field | Mandelbrot | Julia | Burning Ship | Multibrot |
|---|---|---|---|---|
| `CenterRe` | `-0.5` | `0` | `-0.5` | `0` |
| `CenterIm` | `0` | `0` | `-0.5` | `0` |
| `Zoom` | `1` | `1` | `1` | `1` |
| `MaxIter` | `100` | `100` | `100` | `100` |
| `EscapeR` | `2` | `2` | `2` | `2` |
| `JuliaRe` | `-0.123` | `-0.123` | `-0.123` | `-0.123` |
| `JuliaIm` | `0.745` | `0.745` | `0.745` | `0.745` |
| `Exponent` | `3` | `3` | `3` | `3` |

Every field is always populated (even fields the type ignores) so any `DefaultParams`
result serialises to a complete, valid query string.

### `ParseParams(v url.Values) (Params, error)`

1. Read `v.Get("type")`. If empty → error. Otherwise `ParseType` it; on error, propagate.
2. Start from `DefaultParams(thatType)`.
3. For each of `centerRe`, `centerIm`, `zoom`, `escapeR`, `juliaRe`, `juliaIm` (float
   fields) and `maxIter`, `exponent` (int fields): if the key is **absent or empty**,
   keep the default. If present, parse it (`strconv.ParseFloat(s, 64)` /
   `strconv.Atoi(s)`); on a parse error, or a non-finite float (`math.IsNaN` /
   `math.IsInf`), return a non-nil error immediately.
4. **Clamp** (do not reject) the parsed values to their ranges:
   `Zoom → [0.1, 1e12]`, `MaxIter → [1, 1000]`, `EscapeR → [2.0, 10.0]`,
   `Exponent → [2, 8]`. `CenterRe`, `CenterIm`, `JuliaRe`, `JuliaIm` are not clamped
   (any finite value is allowed).
5. Return the resulting `Params`, nil error.

### `ImageQuery(p Params) string`

Builds `url.Values` with keys `type` (=`TypeName(p.Type)`), `centerRe`, `centerIm`,
`zoom`, `maxIter`, `escapeR`, `juliaRe`, `juliaIm`, `exponent`. Floats are formatted with
`strconv.FormatFloat(f, 'g', -1, 64)`, ints with `strconv.Itoa`. Returns
`"/fractal.png?" + values.Encode()` (`Encode` sorts keys alphabetically). The result
parses back through `ParseParams` to an equal `Params` (given in-range inputs).

<!-- verified: go run scratchpad/verify2  =>  ImageQuery(mandelbrot defaults) =>
     "/fractal.png?centerIm=0&centerRe=-0.5&escapeR=2&exponent=3&juliaIm=0.745&juliaRe=-0.123&maxIter=100&type=mandelbrot&zoom=1" -->

### `SaveImage(p Params, dir string, now time.Time) (string, error)`

Holds `saveMu` for the duration. Computes
`name := fmt.Sprintf("%019d-%s.png", now.UnixNano(), TypeName(p.Type))`
(`UnixNano` is at most 19 digits, so `%019d` zero-pads without ever truncating). Renders
`Render(p, ImageWidth, ImageHeight)`, creates `filepath.Join(dir, name)`, and
`png.Encode`s the image into it. On any filesystem/encoding error, returns `("", err)`.
On success returns `(name, nil)` — the base name only, not the full path.

### `ListSaved(dir string) ([]SavedImage, error)`

Reads `dir`. If `dir` does not exist, returns `(nil, nil)` (empty gallery, not an error).
Collects every entry whose name ends in `.png` (case-sensitive) into
`SavedImage{Filename: name, Title: name}`, sorts the slice by `Filename` **descending**
(reverse lexical → newest first, because the zero-padded nanosecond prefix sorts
chronologically), and returns it.

<!-- verified: go run scratchpad/f.go  =>  fmt.Sprintf("%019d-%s.png", 1700000000123456789, "mandelbrot")
     == "1700000000123456789-mandelbrot.png" (19-digit stamp, no pad); %019d of 1 == "0000000000000000001";
     time.Now().UnixNano() is 19 digits. sort.Sort(sort.Reverse(sort.StringSlice)) on zero-padded names
     orders newest (largest) first (go run scratchpad/verify2). -->

### Handlers

All handlers that produce a `PageView` build it the same way (call this **assemble(p)**):
- `Params = p` — verbatim. Every form input's `value` is rendered from `{{.Params.*}}`,
  so this is what makes submitted/parsed values reappear as the new form defaults; a
  zero-value `Params` here renders every numeric field as `0`.
- `ImageURL = ImageQuery(p)`
- `Types` = one `TypeOption` per `FractalType` in constant order
  (`Mandelbrot, Julia, BurningShip, Multibrot`), `Value = TypeName(t)`,
  `Label` = the human label (`"Mandelbrot"`, `"Julia"`, `"Burning Ship"`, `"Multibrot"`),
  `Selected = (t == p.Type)`
- `Saved`, `_ = ListSaved(SavedDir)` (ignore the error — a broken gallery must not break
  the page)
- `ShowJulia = (p.Type == Julia)`
- `ShowExponent = (p.Type == Multibrot)`

**`HandleIndex`** (`GET /`) — `p := DefaultParams(Mandelbrot)`; `RenderPage(w, assemble(p))`.

**`HandleRender`** (`POST /render`) — `r.ParseForm()`; `p, err := ParseParams(r.PostForm)`.
On error: `http.Error(w, err.Error(), http.StatusBadRequest)` and return. Otherwise
`RenderResult(w, assemble(p))`.

**`HandleSelect`** (`POST /select`) — the handler the type `<select>` posts to on change.
`r.ParseForm()`; `t, err := ParseType(r.PostForm.Get("type"))`. On error:
`http.Error(w, err.Error(), http.StatusBadRequest)` and return. Otherwise
`RenderResult(w, assemble(DefaultParams(t)))` — switching type **resets every field to
that type's defaults** (it does not carry the previous type's center/zoom/iter values
forward) and re-renders the image and the now-relevant field set.
<!-- ambiguity class 1 waived 2026-09-06: the checkdesigndoc hit on this paragraph is a
     temporal phrase (a later request reusing an earlier request's values), not a
     geometric or directional claim; the relative-direction regex matched a non-spatial
     word. No coordinate example is applicable here. -->


**`HandleImage`** (`GET /fractal.png`) — `p, err := ParseParams(r.URL.Query())`. On error:
`http.Error(w, err.Error(), http.StatusBadRequest)` and return. Otherwise:
`w.Header().Set("Content-Type", "image/png")`, then
`png.Encode(w, Render(p, ImageWidth, ImageHeight))`. (Status defaults to 200.)

**`HandleSave`** (`POST /save`) — `r.ParseForm()`; `p, err := ParseParams(r.PostForm)`.
On parse error: 400 as above. Otherwise `_, err := SaveImage(p, SavedDir, time.Now())`;
on save error: `http.Error(w, err.Error(), http.StatusInternalServerError)` and return.
On success: `RenderResult(w, assemble(p))` — the response is the same `#app` fragment as
`/render`, but its gallery now includes the just-saved image.

### Templates

`InitTemplates()` parses inline Go string literals (no external `.html` files) into a
`*template.Template` with exactly two named templates, `"page"` and `"result"`. It
`panic`s on a parse error (matching fixture convention). No FuncMap is needed — every
value the templates use is precomputed into `PageView`.

- **`"result"`** is the entire dynamic fragment, wrapped in a single
  `<div id="app"> … </div>`. It contains, in order:
  1. One `<form hx-post="/render" hx-target="#app" hx-swap="outerHTML">` containing:
     - a `<select name="type" hx-post="/select" hx-trigger="change" hx-target="#app" hx-swap="outerHTML">`
       with one `<option value="{{.Value}}">{{.Label}}</option>` per `{{range .Types}}`,
       carrying the ` selected` attribute when `{{.Selected}}`. Changing the select fires
       `POST /select` (not `/render`), which resets the form to the new type's defaults.
     - the always-present numeric inputs: `<input name="centerRe" value="{{.Params.CenterRe}}">`
       and likewise for `centerIm`, `zoom`, `maxIter`, `escapeR` (`maxIter` may use
       `type="number"`, the rest `type="number" step="any"` or `type="text"`);
     - `{{if .ShowJulia}}` … `<input name="juliaRe" value="{{.Params.JuliaRe}}">` and
       `<input name="juliaIm" value="{{.Params.JuliaIm}}">` … `{{end}}` — rendered only
       for the Julia type;
     - `{{if .ShowExponent}}` … `<input name="exponent" value="{{.Params.Exponent}}">` …
       `{{end}}` — rendered only for the Multibrot type;
     - a `<button type="submit">Render</button>`;
     - a second `<button type="submit" hx-post="/save">Save</button>` — same enclosing
       form, so it submits the same field values, but to `/save`. Both buttons inherit the
       form's `hx-target="#app"` / `hx-swap="outerHTML"`.

     When a field is not rendered (e.g. `juliaRe` for a Mandelbrot view), the form does
     not submit that key; `ParseParams` then keeps the type-default for it — which is
     correct, since that field does not affect a Mandelbrot render anyway.
  2. `<img src="{{.ImageURL}}" width="600" height="600" alt="fractal">`.
  3. A gallery section: `{{range .Saved}}` → one
     `<figure><img src="/saved/{{.Filename}}" width="150" alt="{{.Title}}"><figcaption>{{.Title}}</figcaption></figure>`,
     then `{{end}}`. Inside this range `.` is the `SavedImage`; it needs no `$`-prefixed
     root access.
- **`"page"`** is the full document: `<!doctype html>`, a `<head>` with
  `<script src="https://unpkg.com/htmx.org@1.9.12"></script>` and a `<title>`, and a
  `<body>` with an `<h1>` and then `{{template "result" .}}`.

**Swap discipline:** every `POST /render` and `POST /save` response body is exactly the
`"result"` fragment — form, image, and gallery together — and replaces `#app` via
`hx-swap="outerHTML"`. Because the fragment re-declares `<div id="app">` and re-renders
the whole form (with the submitted values as the new defaults), the input box values and
the `<img src>` update together on every submission, with no JavaScript. **All dynamic
state lives inside `#app`; nothing dynamic renders outside it.**

### `main()`

`templates = InitTemplates()`; `os.MkdirAll(SavedDir, 0o755)` (log-fatal on error); build
an `*http.ServeMux` with `GET /{$}` → `HandleIndex`, `POST /render` → `HandleRender`,
`POST /select` → `HandleSelect`, `GET /fractal.png` → `HandleImage`,
`POST /save` → `HandleSave`, and
`GET /saved/` → `http.StripPrefix("/saved/", http.FileServer(http.Dir(SavedDir)))`;
then `http.ListenAndServe(":8080", mux)`.

## Domain-Specific Test Scenarios

Coordinate mapping (used by every scenario below): image is 600 wide × 600 tall. Pixel
`px` increases rightward, real part increases with it. Pixel `py` increases **downward**,
imaginary part **decreases** with it (`py = 0` is the top = largest imaginary value). The
sampled point is the pixel centre (`+ 0.5` on each axis).

### Required test scenarios for the `fractal-core` bead (`Escape`)

All with `MaxIter = 100`, `EscapeR = 2`, and the type's `DefaultParams` for the per-type
fields unless stated. "in-set" means `Escape` returns exactly `100`.

**Mandelbrot escape counts:**
- `point = 0+0i` → `100` (origin is in the main cardioid).
- `point = -1+0i` → `100` (period-2 bulb).
- `point = 2+2i` → `1`. Orbit: `z0 = 0` (n=0, `0 ≤ 4`), `z1 = 2+2i` (n=1,
  `|z1|² = 8 > 4` → return 1).
- `point = 1+0i` → `3`. Orbit `0 → 1 → 2 → 5`; `4 ≤ 4` at n=2 (strict `>`), `25 > 4` at
  n=3. Do NOT return `2` (that is the test-after-update off-by-one) or `4`.

**Julia escape counts** (`JuliaRe = -0.123`, `JuliaIm = 0.745` — the Douady rabbit):
- `point = 0+0i` → `100` (in-set). This only holds because Julia sets `z0 = point` and
  `c = (JuliaRe, JuliaIm)`; the Mandelbrot-style `z0 = 0, c = point` gives a different
  (and here escaping) result.
- `point = 10+10i` → `0` — `z0 = point` already has `|z0|² = 200 > 4`, so the escape test
  passes at n=0 before any update.
- `point = 2+0i` → `1`.

**Burning Ship escape counts** (`CenterRe = -0.5`, `CenterIm = -0.5` defaults; those don't
affect `Escape`, only the view):
- `point = 0+0i` → `100` (`c = 0`, orbit stays at 0).
- `point = -0.5-0.5i` → `100` (in-set).
- `point = 2+2i` → `1`.
- `point = 1+1i` → `2`. Requires abs-ing the components of `z` before squaring; abs-ing
  `z*z` instead gives a different count.

**Multibrot escape counts** (`Exponent = 3`):
- `point = 0+0i` → `100` (in-set).
- `point = 1+0i` → `3`.
- `point = 1.5+0i` → `2`. Requires the integer cube by repeated multiplication;
  `cmplx.Pow` disagrees.

### Required test scenarios for the `render` bead (`PixelToComplex`, `Color`, `Render`)

**`PixelToComplex`, `Params{CenterRe: -0.5, CenterIm: 0, Zoom: 1}`, width = height = 600:**
- pixel `(0, 0)` → `(-2.4966666666666666, +1.9966666666666666)` — top-left, largest
  imaginary. Do NOT get `(-2.5, +2.0)` (that is the version without the `+ 0.5`
  pixel-centre offset) and do NOT get imaginary `-1.9966666666666666` (that is the
  flipped-sign version — `py = 0` must be the *top*, i.e. the *largest* imaginary value).
- pixel `(300, 300)` → `(-0.49666666666666665, -0.0033333333333333335)` — just past the
  centre because 600 is even (no exact centre pixel).
- pixel `(599, 599)` → `(+1.4966666666666666, -1.9966666666666666)` — bottom-right,
  smallest imaginary.

**`Color`, `maxIter = 100`:**
- `Color(0, 100)` → `color.RGBA{0, 0, 255, 255}` (escaped-immediately is pure blue, NOT
  black).
- `Color(50, 100)` → `color.RGBA{127, 127, 255, 255}` (`255*50/100 = 127` by integer
  division).
- `Color(99, 100)` → `color.RGBA{252, 252, 255, 255}`.
- `Color(100, 100)` → `color.RGBA{0, 0, 0, 255}` (in-set is black; `n >= maxIter`).

**`Render`, `DefaultParams(Mandelbrot)`, 600×600:**
- Result bounds are `image.Rect(0, 0, 600, 600)`.
- Pixel `(300, 300)` is `color.RGBA{0, 0, 0, 255}` — it maps to
  `(-0.49666…, -0.00333…)`, which is inside the main cardioid, so `Escape` returns `100`
  and `Color` returns black.
- Pixel `(0, 0)` is **not** black — it maps to `(-2.4966…, +1.9966…)`, which escapes at
  n=1, so `Color(1, 100) = {2, 2, 255, 255}`.

### Required test scenarios for the `save` bead (`SaveImage`, `ListSaved`)

- `SaveImage(DefaultParams(Mandelbrot), tmpDir, time.Unix(0, 1_700_000_000_123_456_789))`
  returns `("1700000000123456789-mandelbrot.png", nil)` and that file exists in
  `tmpDir`, is a decodable PNG, and has bounds `image.Rect(0, 0, 600, 600)`. (`UnixNano`
  for any real time since 2023 is exactly 19 digits, so `%019d` adds no leading zeros
  here; it only pads a small synthetic value like the two below.)
- After writing two files named `0000000000000000001-julia.png` and
  `0000000000000000002-julia.png` into a temp dir, `ListSaved` returns them in the order
  `["0000000000000000002-julia.png", "0000000000000000001-julia.png"]` (newest — larger
  timestamp — first) and ignores any non-`.png` file in the same dir.
- `ListSaved` on a path that does not exist returns `(nil, nil)` — no error.

### Required test scenarios for the `integration` bead

- Using `httptest.NewServer` with the real mux: `GET /fractal.png?type=mandelbrot`
  (no other params — `ParseParams` fills the rest from `DefaultParams(Mandelbrot)`)
  returns HTTP 200, `Content-Type: image/png`, and a body that `png.Decode`s to an image
  with bounds `image.Rect(0, 0, 600, 600)` whose pixel `(300, 300)` is opaque black
  (`R = G = B = 0`, `A = 255` — read via `color.RGBAModel` / `.RGBA()` shifted to 8-bit).

## Cross-Bead Contracts

### fractal-core → render (data-shape)

- **type**: data-shape
- **producer**: fractal-core (`fractal.go`)
- **consumer**: render (`render.go`)
- **interface**: `Params{Type FractalType; CenterRe, CenterIm, Zoom float64; MaxIter int; EscapeR float64; JuliaRe, JuliaIm float64; Exponent int}` and `func Escape(p Params, point complex128) int` and the four `FractalType` constants `Mandelbrot, Julia, BurningShip, Multibrot` (`Mandelbrot == 0`).
- **notes**: `Render` calls `PixelToComplex` then `Escape` then `Color` for every pixel,
  passing the same `Params` to all three. `Escape` returns exactly `p.MaxIter` for an
  in-set point; `Color` treats `n >= maxIter` as in-set. `render.go` must not
  re-implement the iteration — it calls `Escape`.

### render → handlers (protocol)

- **type**: protocol
- **producer**: render (`render.go`)
- **consumer**: handlers (`handlers.go`)
- **interface**: `func Render(p Params, width, height int) *image.RGBA`
- **notes**: `HandleImage` calls `Render(p, ImageWidth, ImageHeight)` and then
  `png.Encode(w, img)` — after `w.Header().Set("Content-Type", "image/png")` and before
  writing any body byte. `Render` never returns nil; there is no error path. On a
  `ParseParams` error `HandleImage` responds `400` and never calls `Render`. `save.go`'s
  `SaveImage` also calls `Render(p, ImageWidth, ImageHeight)` and `png.Encode`s the result
  to a file — the same producer, a second consumer.

### params → handlers (protocol)

- **type**: protocol
- **producer**: params (`params.go`)
- **consumer**: handlers (`handlers.go`)
- **interface**: `func ParseParams(v url.Values) (Params, error)` and `func ImageQuery(p Params) string`
- **notes**: `HandleImage` parses from `r.URL.Query()`; `HandleRender` and `HandleSave`
  call `r.ParseForm()` and parse from `r.PostForm`. `HandleSelect` reads only
  `r.PostForm.Get("type")` through `ParseType` and then uses `DefaultParams` — it does not
  call `ParseParams`. Every handler that produces a `PageView` sets
  `ImageURL = ImageQuery(p)`. A non-nil `ParseParams` error becomes an
  HTTP 400 (`http.Error` with the error text); it is never rendered as a normal page.
  `ImageQuery(p)` must round-trip: `ParseParams(parsed query of ImageQuery(p)) == p` for
  in-range `p`.

### save → handlers (protocol)

- **type**: protocol
- **producer**: save (`save.go`)
- **consumer**: handlers (`handlers.go`)
- **interface**: `func SaveImage(p Params, dir string, now time.Time) (string, error)` and `func ListSaved(dir string) ([]SavedImage, error)`, `SavedImage{Filename, Title string}`, `const SavedDir = "saved"`
- **notes**: `HandleSave` calls `SaveImage(p, SavedDir, time.Now())`; a non-nil error →
  HTTP 500, no fragment written. On success it falls through to rendering the `"result"`
  fragment, whose gallery is rebuilt via `ListSaved(SavedDir)` and therefore includes the
  new file. `HandleIndex`/`HandleRender`/`HandleSave` all call `ListSaved(SavedDir)` and
  **ignore its error** (a broken gallery must not break the page — treat as empty).

### handlers → templates (data-shape)

- **type**: data-shape
- **producer**: handlers (`handlers.go` — assembles `PageView`)
- **consumer**: templates (`templates.go`)
- **interface**: `PageView{Params Params; ImageURL string; Types []TypeOption; Saved []SavedImage; ShowJulia bool; ShowExponent bool}`, `TypeOption{Value, Label string; Selected bool}`, `SavedImage{Filename, Title string}`
- **notes**: No FuncMap — all display values are precomputed (`Selected`/`ShowJulia`/
  `ShowExponent` bools, `ImageURL` string, `Title` strings). Both `"page"` and `"result"`
  consume the same `PageView`; `"page"` renders `"result"` via `{{template "result" .}}`.
  Inside `{{range .Types}}` and `{{range .Saved}}` the loop variable `.` is the element;
  neither loop needs `$`-prefixed root access, but the `{{if .ShowJulia}}` /
  `{{if .ShowExponent}}` blocks are at the top level (outside any range) so `.ShowJulia`
  resolves against the root `PageView` directly — if a per-type input is ever moved inside
  a `{{range}}` it must switch to `$.ShowJulia`. All dynamic content (`<form>`, `<img>`,
  gallery) renders **inside** `<div id="app">`, the `hx-target`; the `POST /render`,
  `POST /select`, and `POST /save` responses are the `"result"` fragment only and replace
  `#app` via `hx-swap="outerHTML"`. The type `<select>` carries its own
  `hx-post="/select"` overriding the form's `hx-post`; the enclosed `Save` button carries
  `hx-post="/save"`. htmx serialises the enclosing `<form>` for a button-triggered submit,
  so both buttons and the select post the current field values without an explicit
  `hx-include`.

### handler → image endpoint (format)

- **type**: format
- **producer**: handlers (`HandleImage`) + render
- **consumer**: any HTTP client (the `<img>` tag; the integration test)
- **interface**: `GET /fractal.png?type=…&centerRe=…&…` → HTTP 200, header
  `Content-Type: image/png`, body = a standard PNG (`\x89PNG\r\n\x1a\n` magic) that
  `image/png.Decode` accepts, image bounds `image.Rect(0, 0, 600, 600)`.
- **notes**: Round-trip check belongs to the integration bead: `png.Decode` the response
  body and assert bounds plus one pixel. Malformed query → HTTP 400 with a text body, not
  a PNG.

## Decomposition Notes

**Bead dependency order (do not reorder):**

1. **fractal-core** — `FractalType` + constants, `Params`, `TypeName`, `ParseType`,
   `DefaultParams`, `Escape`. No dependencies on any other bead. Owns `fractal.go`.
2. **render** — `ImageWidth`, `ImageHeight`, `PixelToComplex`, `Color`, `Render`. Depends
   on bead 1 (`Params`, `Escape`). Owns `render.go`.
3. **params** — `ParseParams`, `ImageQuery`. Depends on bead 1 (`Params`, `TypeName`,
   `ParseType`, `DefaultParams`). Owns `params.go`.
4. **save** — `SavedImage`, `SaveImage`, `ListSaved`, `SavedDir`, `saveMu`. Depends on
   beads 1 and 2 (`Params`, `Render`). Owns `save.go`.
5. **templates** — `InitTemplates`, `RenderPage`, `RenderResult`. Depends on bead 6's
   `PageView`/`TypeOption` types **only** for the data shape — decompose templates
   **before** handlers so the handler bead's httptest assertions run against real template
   output, per the guide's ordering rule. Owns `templates.go`. If SURVEY places
   `PageView`/`TypeOption` such that a template↔handler type cycle appears, the types
   belong in `handlers.go` and the template bead references them across the file boundary
   in the same package (no import needed).
6. **handlers** — `TypeOption`, `PageView`, `HandleIndex`, `HandleRender`, `HandleSelect`,
   `HandleImage`, `HandleSave`. Depends on beads 2, 3, 4, 5. Every handler bead exit
   criterion is an `httptest` smoke test (not `go build`): at minimum one request/response
   per handler, asserting status + one structural property — for `/fractal.png`:
   `Content-Type: image/png` and a successful `png.Decode`; for `GET /`, `/render`,
   `/select`, `/save`: the response body contains `id="app"` and an `<img` tag. Add one
   assertion that `POST /select` with `type=julia` produces a body containing
   `name="juliaRe"` and that `GET /` (Mandelbrot) does **not**. Owns `handlers.go`.
7. **main** — `var templates`, `func main()`. Wires the mux. Depends on bead 6. Owns
   `main.go`.
8. **integration** — one bounded `httptest` scenario (below).

**Integration bead — one bounded scenario:** stand up `httptest.NewServer` with the real
mux; `GET /fractal.png?type=mandelbrot`; assert HTTP 200, `Content-Type: image/png`,
`png.Decode` succeeds with bounds `image.Rect(0, 0, 600, 600)`, and decoded pixel
`(300, 300)` is opaque black. Do **not** also test `/render`, `/save`, and the gallery in
this bead — those are covered by the handlers bead.

**Must not bind to a fixed port in any test** — use `httptest` throughout.

**Pins (verbatim worked values — carry into the named bead's spec exactly, do not
re-derive or paraphrase):**

- **Pin — `fractal-core` bead, `Escape` (Mandelbrot):** with `MaxIter = 100`,
  `EscapeR = 2`: `Escape` for `point = 0+0i` → `100`; `-1+0i` → `100`; `2+2i` → `1`;
  `1+0i` → `3` (NOT `2`, NOT `4` — the escape test is `real²+imag² > 4` strict, evaluated
  before each update; orbit `0 → 1 → 2 → 5`).
- **Pin — `fractal-core` bead, `Escape` (Julia, c = -0.123 + 0.745i):** with
  `MaxIter = 100`, `EscapeR = 2`, `JuliaRe = -0.123`, `JuliaIm = 0.745`: `point = 0+0i` →
  `100`; `point = 10+10i` → `0` (z0 = point already outside, escapes at n=0);
  `point = 2+0i` → `1`. Julia uses `z0 = point`, `c = (JuliaRe, JuliaIm)`.
- **Pin — `fractal-core` bead, `Escape` (Burning Ship):** with `MaxIter = 100`,
  `EscapeR = 2`: `point = 0+0i` → `100`; `point = -0.5-0.5i` → `100`; `point = 2+2i` →
  `1`; `point = 1+1i` → `2`. Update abs's `Re z` and `Im z` **before** squaring.
- **Pin — `fractal-core` bead, `Escape` (Multibrot, Exponent = 3):** with `MaxIter = 100`,
  `EscapeR = 2`, `Exponent = 3`: `point = 0+0i` → `100`; `point = 1+0i` → `3`;
  `point = 1.5+0i` → `2`. Power is `z*z*z` by repeated multiplication, not `cmplx.Pow`.
- **Pin — `render` bead, `PixelToComplex`:** with `Params{CenterRe: -0.5, CenterIm: 0,
  Zoom: 1}`, `width = height = 600`: pixel `(0,0)` →
  `(-2.4966666666666666, +1.9966666666666666)`; pixel `(300,300)` →
  `(-0.49666666666666665, -0.0033333333333333335)`; pixel `(599,599)` →
  `(+1.4966666666666666, -1.9966666666666666)`. `py = 0` is the top = largest imaginary;
  the `+ 0.5` pixel-centre offset is required.
- **Pin — `render` bead, `Color`:** with `maxIter = 100`: `Color(0, 100)` →
  `color.RGBA{0, 0, 255, 255}` (NOT black); `Color(50, 100)` →
  `color.RGBA{127, 127, 255, 255}`; `Color(99, 100)` → `color.RGBA{252, 252, 255, 255}`;
  `Color(100, 100)` → `color.RGBA{0, 0, 0, 255}` (in-set black; `n >= maxIter`).
- **Pin — `render` bead, `Render`:** `Render(DefaultParams(Mandelbrot), 600, 600)` has
  bounds `image.Rect(0, 0, 600, 600)`; pixel `(300, 300)` is `color.RGBA{0, 0, 0, 255}`;
  pixel `(0, 0)` is `color.RGBA{2, 2, 255, 255}` (escapes at n=1).
- **Pin — `save` bead, `SaveImage`/`ListSaved`:**
  `SaveImage(DefaultParams(Mandelbrot), tmpDir, time.Unix(0, 1700000000123456789))` →
  `("1700000000123456789-mandelbrot.png", nil)` (name = `fmt.Sprintf("%019d-%s.png", 1700000000123456789, "mandelbrot")`,
  and 1700000000123456789 is already 19 digits so no zero-padding is added); the file
  decodes as a 600×600 PNG. `ListSaved` returns `.png` entries sorted by filename
  **descending** (newest first) and returns `(nil, nil)` for a missing directory.
- **Pin — `integration` bead:** `GET /fractal.png?type=mandelbrot` → HTTP 200,
  `Content-Type: image/png`, body `png.Decode`s to bounds `image.Rect(0, 0, 600, 600)`,
  pixel `(300, 300)` opaque black.

## Open Questions

1. **Gallery scope.** This draft's gallery is view-only (thumbnails, no interaction).
   Out of scope for v1: clicking a saved image to reload its parameters into the form
   (would require a saved-parameters sidecar file per image). OK to leave out?

<!-- Resolved 2026-09-06 (draft-design-doc interactive pass):
  - Escaped-pixel palette: blue→white ramp RGBA{v,v,255,255}, in-set black. (kept as drafted)
  - Julia default c: Douady rabbit -0.123 + 0.745i. (kept as drafted)
  - Save UX: server-side saved/ dir + gallery served at GET /saved/. (kept as drafted)
  - Per-type form fields: show/hide per selected type via POST /select (HandleSelect,
    ShowJulia/ShowExponent on PageView) — changed from the drafted "always show all". -->

