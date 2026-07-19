# Fractal PNG Generator — Design Document

## Overview

A Go library that renders fractal images to PNG files: two escape-time fractals
(Mandelbrot set, Julia set) and one recursive geometric fractal (Sierpinski
triangle). No CLI argument parsing, no animation, no additional fractal types,
no color palettes beyond grayscale/white-on-black — all out of scope.

**Domain parameters:** Canvas dimensions, iteration counts, and all fractal
parameters (center point, scale, Julia constant, recursion depth) are always
supplied by the caller as function arguments — there are no default values
baked into the library.

## Data Types and Function Signatures

All `.go` source files in this project use `package main`. The module name is
`fractal`.

```go
// GenerateMandelbrot renders a Mandelbrot set escape-time fractal.
// centerReal/centerImag is the complex-plane point at the canvas center.
// scale is the complex-plane distance represented by one pixel.
func GenerateMandelbrot(width, height, maxIterations int, centerReal, centerImag, scale float64) *image.RGBA

// GenerateJulia renders a Julia set escape-time fractal for the fixed
// complex constant c = (cReal, cImag). centerReal/centerImag/scale map
// pixels to the complex plane exactly as in GenerateMandelbrot.
func GenerateJulia(width, height, maxIterations int, cReal, cImag, centerReal, centerImag, scale float64) *image.RGBA

// GenerateSierpinski renders a Sierpinski triangle via recursive edge
// subdivision to the given recursion depth (depth 0 draws a single triangle).
func GenerateSierpinski(width, height, depth int) *image.RGBA

// SavePNG encodes img as a PNG and writes it to path, overwriting any
// existing file there.
func SavePNG(img image.Image, path string) error
```

### Export signatures

```go
var _ func(int, int, int, float64, float64, float64) *image.RGBA = GenerateMandelbrot
var _ func(int, int, int, float64, float64, float64, float64, float64) *image.RGBA = GenerateJulia
var _ func(int, int, int) *image.RGBA = GenerateSierpinski
var _ func(image.Image, string) error = SavePNG
```

## Behavioral Specification

**`GenerateMandelbrot`** — For each pixel `(px, py)` with `0 ≤ px < width`,
`0 ≤ py < height`:

1. Map the pixel to a complex point `c`:
   `cReal = centerReal + (float64(px) - float64(width)/2) * scale`
   `cImag = centerImag + (float64(py) - float64(height)/2) * scale`
2. Iterate `z` starting at `zReal, zImag = 0, 0`. For iteration `i` from `0`
   to `maxIterations-1`:
   `newReal = zReal*zReal - zImag*zImag + cReal`
   `newImag = 2*zReal*zImag + cImag`
   set `zReal, zImag = newReal, newImag`, then check escape:
   if `zReal*zReal + zImag*zImag > 4`, the point escaped at iteration `i`;
   stop iterating.
3. Coloring: if the point escaped at iteration `i` (`i < maxIterations`),
   set the pixel to grayscale `RGBA{R: v, G: v, B: v, A: 255}` where
   `v = uint8(255 * i / maxIterations)`. If the point never escaped (all
   `maxIterations` iterations completed without exceeding the threshold),
   set the pixel to black: `RGBA{0, 0, 0, 255}`.

**`GenerateJulia`** — Uses the exact same escape-time iteration formula and
coloring rule as `GenerateMandelbrot` above, but with the roles of `z` and
`c` swapped: `z` starts at the **mapped pixel point** (computed via
`centerReal`/`centerImag`/`scale` exactly as `GenerateMandelbrot` computes
`c`), and `c` is the **fixed constant** `(cReal, cImag)` for every pixel in
the image. Do not swap this: starting `z` at `(0,0)` and using the mapped
pixel as `c` (i.e., reusing `GenerateMandelbrot`'s exact formula) produces a
Mandelbrot-shaped image regardless of the `cReal`/`cImag` parameters, not a
Julia set — the whole point of Julia's parameterization is that `z` varies
per-pixel and `c` is constant across the image.

**`GenerateSierpinski`** — Recursively subdivides a triangle:

1. The initial (depth-0-of-recursion) triangle vertices for the whole canvas
   are: `top = (width/2, height/10)`, `bottomLeft = (width/10, height*9/10)`,
   `bottomRight = (width*9/10, height*9/10)` (integer pixel coordinates,
   integer division is fine here).
2. Recursive step `sierpinski(p1, p2, p3, remainingDepth)`:
   - If `remainingDepth == 0`: draw all three edges (`p1`–`p2`, `p2`–`p3`,
     `p3`–`p1`) in white (`RGBA{255,255,255,255}`) onto a black background,
     using any standard line-rasterization approach (e.g. Bresenham's
     algorithm). This is the base case — return without recursing further.
   - If `remainingDepth > 0`: compute the three edge midpoints
     `m12 = midpoint(p1,p2)`, `m23 = midpoint(p2,p3)`, `m13 = midpoint(p1,p3)`
     (integer midpoint: `((x1+x2)/2, (y1+y2)/2)`). Recurse into the three
     **corner** sub-triangles: `(p1, m12, m13)`, `(m12, p2, m23)`,
     `(m13, m23, p3)`, each with `remainingDepth-1`. **Do NOT recurse into
     the middle sub-triangle** `(m12, m23, m13)` — it must remain the
     background color. Recursing into all four sub-triangles (including the
     middle one) produces a solid filled triangle outline pattern instead of
     the Sierpinski pattern, which is the single most common mistake in a
     naive implementation of this algorithm.
3. `GenerateSierpinski` calls this recursive step once, starting from the
   canvas-covering triangle in step 1 with `remainingDepth = depth`, onto a
   canvas initialized entirely to black (`RGBA{0,0,0,255}`) before drawing.

**`SavePNG`** — Encodes `img` using `image/png.Encode` and writes the result
to a new file at `path` (creating or truncating it), returning any error from
file creation or encoding.

## Cross-Bead Contracts

- **type**: format
- **producer**: any generator (`GenerateMandelbrot`, `GenerateJulia`,
  `GenerateSierpinski`) together with `SavePNG`
- **consumer**: integration test
- **interface**: `SavePNG` writes a standard PNG file (via `image/png.Encode`)
  that must be exactly recoverable via `image/png.Decode` — decoding the
  saved file and reading a pixel via `.At(x, y)` must return the identical
  color that was set in the source image at that pixel.
- **notes**: PNG encoding is lossless for `*image.RGBA` source images, so
  `decode(encode(img))` must match `img` exactly at every tested pixel — no
  approximate/tolerance-based comparison is needed or expected.

## Decomposition Notes

**Integration bead scope:** Generate a small (e.g. 21×21) Mandelbrot image
with `centerReal=-0.5, centerImag=0, scale=0.1, maxIterations=50`; save it to
a temp PNG file; decode it back; verify the pixel at the exact image center
(`px=10, py=10`, corresponding to complex point `(-0.5, 0)`, which is deep
inside the Mandelbrot set and never escapes) decodes to black
`RGBA{0,0,0,255}`.