# <Project Name> — Design Document

## Overview

One paragraph: what does this project do, who uses it, what is the runtime model
(CLI, server, library, etc.), and what is explicitly out of scope.

## Architecture

Show the directory layout and file responsibilities. Be explicit about which file
owns which concern — this drives output_files assignments during decomposition.

```
project/
├── go.mod
├── main.go     — entry point
├── foo.go      — core logic
├── foo_test.go — tests for foo.go
└── ...
```

## Data Types and Function Signatures

List every exported type, constant, and function with complete Go signatures.
DECOMPOSE_SPEC uses this section to generate the compile-time assertions in
`api_check_test.go`. Be precise — wrong parameter or return types here propagate
through the whole project.

```go
package main

type Foo struct {
    Bar string
    Baz int
}

func NewFoo(bar string) *Foo
func (f *Foo) Process(input []byte) ([]byte, error)
```

### Compile-time assertions for api_check_test.go

Copy these exact lines into this section after writing the signatures above.
DECOMPOSE_SPEC will include them verbatim in the layout Bead's full_text.

```go
var _ func(bar string) *Foo = NewFoo
var _ func(*Foo, []byte) ([]byte, error) = (*Foo).Process
```

## Cross-Bead Contracts

List every interface that one Bead produces and another Bead consumes. DECOMPOSE_SPEC
uses this section to set exit criteria and to populate consumer Bead specs with the
exact interface text. AUDIT uses it to verify that every contract has adequate test
coverage. Omit this section entirely if no such interfaces exist.

Each entry must declare:
- **type**: `data-shape` | `format` | `protocol` | `schema`
- **producer**: title of the producing Bead (must match a Bead title exactly)
- **consumer**: title of the consuming Bead (must match a Bead title exactly)
- **interface**: the exact specification — struct definition, field list, named template
  strings, function signatures, or schema excerpt
- **notes** *(optional)*: scoping rules, required helper registrations, naming conventions,
  version constraints, or anything the consumer Bead must know that isn't captured in the
  interface field alone

Contract types and what DECOMPOSE_SPEC does with each:

| Type | What it is | What DECOMPOSE_SPEC does |
|------|------------|--------------------------|
| `data-shape` | A struct or field set passed from one Bead to another (e.g. handler → template view model) | Quotes interface verbatim in consumer Bead spec; requires a render/instantiation test in consumer exit criteria |
| `format` | A serialization format shared by a writer and a reader (encode/decode, marshal/unmarshal) | Adds a round-trip integration Bead after the pair; round-trip tests excluded from individual Bead exit criteria |
| `protocol` | A request/response exchange (HTTP, RPC, message queue) | Adds an integration Bead that sends a message through both sides |
| `schema` | A validation schema (JSON Schema, protobuf, SQL DDL) | Requires tests against a valid and an invalid document in the producing Bead's exit criteria |

### Example entries

#### Handler → template view model
- **type**: data-shape
- **producer**: http-handlers
- **consumer**: templates
- **interface**: `GameView{Board [8][8]*Piece, Selected *Square, ValidDests map[[2]int]bool, Message string, GameOver bool}`
- **notes**: Inside `{{range}}` loops, top-level GameView fields must use `$` prefix
  (e.g. `$.Selected`, `$.ValidDests`). Template must register FuncMap helpers
  `add(a, b int) int` and `mod(a, b int) int` before parsing.

#### Encoder → decoder (round-trip)
- **type**: format
- **producer**: encoder
- **consumer**: decoder
- **interface**: Little-endian binary: `[4]byte magic | uint32 length | []byte payload`
- **notes**: Zero-length payload is valid; magic must be exactly `\x89PNG`.

#### Event producer → event consumer (protocol)
- **type**: protocol
- **producer**: publisher
- **consumer**: subscriber
- **interface**: JSON object `{"topic": string, "payload": any, "ts": RFC3339}`
- **notes**: `ts` is always UTC. Consumer must tolerate unknown fields (forward compatibility).

## Decomposition Notes

Use this section to override DECOMPOSE_SPEC's generic decomposition heuristics. Guidance
here is authoritative — it supersedes the generic rules. Include it only when the generic
rules would produce wrong bead boundaries for this project.

Common uses:
- Specify exact bead boundaries when multiple beads share a file (sequential dependency)
- Designate which bead owns cross-function integration tests
- Call out constraints on individual beads (e.g. "bead 8 must not embed HTML inline")

### Bead list

| # | Title | Output files | Exit criterion |
|---|-------|-------------|----------------|
| 1 | layout | foo.go, main.go, go.mod, api_check_test.go | `go build ./...` |
| 2 | ... | ... | ... |

### Bead boundaries and rules

Prose notes on sequential dependencies, shared files, and any per-bead constraints
that DECOMPOSE_SPEC must follow exactly.