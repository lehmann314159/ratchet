# bead 314 — parser

impl files: parser.go
required test funcs: TestParser

good/ holds the baseline-8 final implementation (test must PASS against it).

Create 2–3 sibling dirs m1_<desc>/, m2_<desc>/, ... each containing a full copy
of the impl file(s) above with ONE realistic defect injected (off-by-one, wrong
operator, dropped nil-check, wrong boundary). The WRITE grader scores a run as
passing only if the generated test compiles, passes good/, and fails ≥1 mutant.
