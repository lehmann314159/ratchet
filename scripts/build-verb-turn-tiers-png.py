#!/usr/bin/env python3
"""Diagram: the three-tier turn-evaluation split across ratchet's verbs.
Standalone explainer image, not tied to any single doc (unlike
docs/diagrams/*.png, which illustrate docs/ratchet_state_machine.md
specifically). Styled to match scripts/build-state-machine-diagram-pngs.py's
palette for visual continuity, but self-contained (no cross-file import)
since this content is unrelated to the state-machine FSM diagrams.

Ground truth for the grouping (verified against source, not guessed):
- Tier 1 (single Chat() call): SURVEY_SPEC, VERIFY_MANIFEST, CERTIFY_MANIFEST,
  DECOMPOSE_SPEC, AUDIT_DECOMPOSITION, RECONCILE_DECOMPOSITION,
  ANALYZE_EXECUTION, COMPRESS_ANALYSIS, REVISE_PENDING.
- Tier 2 (ChatWithTools, bounded completion-check loop): REFINE_TESTS_WRITE
  (refine_tests.go:628), REFINE_TESTS_CRITIQUE (refine_tests.go:1254),
  REFINE_TESTS_JUDGE (refine_tests.go:1507), ADJUDICATE_NEXT_EXECUTION
  (adjudicate_next_execution.go:1235) — each loops ChatWithTools up to
  snippetVerificationTurns/maxTurns (~6), asking one yes/no completion
  question per turn, and stops at the cap regardless of outcome.
- Tier 3 (ChatWithTools, stall/productivity supervision): EXECUTE_BEAD only —
  progressTracker (novel write_file content-hash), 12m checkpoint / 45m
  ceiling, ContentStallTimeout, plus the external MONITOR_EXECUTION watchdog
  subprocess that can kill it independently.

Run: pip install matplotlib; python3 scripts/build-verb-turn-tiers-png.py
Output: docs/diagrams/verb-turn-tiers.png
"""
import os
import matplotlib
matplotlib.use("Agg")
import matplotlib.pyplot as plt
from matplotlib.patches import FancyBboxPatch, Circle, FancyArrowPatch
from matplotlib.textpath import TextPath
from matplotlib.font_manager import FontProperties

OUT = os.path.join(os.path.dirname(__file__), "..", "docs", "diagrams", "verb-turn-tiers.png")

# ---- palette (mirrors writing/decks/src/deck_common.py) ----
INK      = "#1C1E22"
INK_DIM  = "#5F636B"
GROUND   = "#F6F5F2"
SURFACE  = "#FFFFFF"
SURFACE2 = "#F1EFEA"
LINE     = "#E2E0DA"
ACCENT   = "#3D6D7E"
OK       = "#4F7A3F"
WARN     = "#B9781A"
STOP     = "#A83A2A"
WHITE    = "#FFFFFF"

TITLE_FONT = "Georgia"
BODY_FONT = "Helvetica Neue"
MONO_FONT = "Menlo"

W, H = 13.333, 7.5
DPI = 220


def text_width_in(s, size_pt, font=MONO_FONT, bold=True):
    tp = TextPath((0, 0), s, size=size_pt,
                   prop=FontProperties(family=font, weight="bold" if bold else "normal"))
    return tp.get_extents().width / 72.0


fig, ax = plt.subplots(figsize=(W, H), dpi=DPI)
# Full-bleed axes: without this, matplotlib's default margins compress the
# data coordinate system relative to the physical figure, so 1 data-unit no
# longer equals 1 inch — but text (sized in absolute points) doesn't shrink
# with it, so any box sized to "fit" a measured text width overflows. This
# makes data-space and physical inches coincide exactly, matching how
# text_width_in's TextPath measurement (also absolute) is used below.
fig.subplots_adjust(left=0, right=1, top=1, bottom=0)
ax.set_xlim(0, W)
ax.set_ylim(0, H)
ax.invert_yaxis()
ax.axis("off")
fig.patch.set_facecolor(GROUND)
ax.set_facecolor(GROUND)

# ---------------------------------------------------------------- header
ax.text(0.7, 0.42, "RATCHET · VERB EXECUTION MODEL", fontsize=11, fontweight="bold",
         fontname=MONO_FONT, color=ACCENT, ha="left", va="top")
ax.text(0.7, 0.95, "Three tiers of turn evaluation", fontsize=25, fontweight="bold",
         fontname=TITLE_FONT, color=INK, ha="left", va="top")
ax.plot([0.7, 12.63], [1.35, 1.35], color=LINE, linewidth=1)

# ---------------------------------------------------------------- footer
ax.plot([0.7, 12.63], [7.18, 7.18], color=LINE, linewidth=1)
ax.text(0.7, 7.30, "RATCHET · VERB EXECUTION MODEL", fontsize=8.5, fontname=MONO_FONT,
         color=INK_DIM, ha="left", va="top")


def chip(x, y, w, h, label, border, fill=SURFACE2, font_size=9, text_color=INK):
    box = FancyBboxPatch((x, y), w, h, boxstyle="round,pad=0,rounding_size=0.06",
                          linewidth=1.1, edgecolor=border, facecolor=fill, zorder=3)
    ax.add_patch(box)
    ax.text(x + w / 2, y + h / 2, label, fontsize=font_size, fontname=MONO_FONT,
             fontweight="bold", color=text_color, ha="center", va="center", zorder=4)


def pack_chips(labels, x0, y0, max_right, h, gap, vgap, border, font_size=8.5, pad=0.18):
    """Lay out chips left-to-right, wrapping to a new row when the next chip
    would cross max_right. Returns the y just below the last row used."""
    x, y = x0, y0
    row_used = False
    for lab in labels:
        w = text_width_in(lab, font_size, MONO_FONT, bold=True) + pad
        if row_used and x + w > max_right:
            x = x0
            y += h + vgap
        chip(x, y, w, h, lab, border, font_size=font_size)
        x += w + gap
        row_used = True
    return y + h


def band(y0, h, n, name, subtitle, color):
    bg = FancyBboxPatch((0.7, y0), 11.93, h, boxstyle="round,pad=0,rounding_size=0.06",
                         linewidth=1, edgecolor=LINE, facecolor=SURFACE, zorder=1)
    ax.add_patch(bg)
    cy = y0 + 0.34
    ax.add_patch(Circle((1.16, cy), 0.22, facecolor=color, edgecolor="none", zorder=3))
    ax.text(1.16, cy, str(n), fontsize=13, fontweight="bold", fontname=BODY_FONT,
             color=WHITE, ha="center", va="center", zorder=4)
    ax.text(1.56, y0 + 0.20, f"Tier {n} — {name}", fontsize=15, fontweight="bold",
             fontname=TITLE_FONT, color=INK, ha="left", va="top", zorder=3)
    ax.text(1.56, y0 + 0.53, subtitle, fontsize=11, style="italic", fontname=BODY_FONT,
             color=INK_DIM, ha="left", va="top", zorder=3)


MAX_RIGHT = 12.35
CHIP_H = 0.34

# ---------------------------------------------------------------- Tier 1
y0 = 1.55
band_h1 = 2.15
band(y0, band_h1, 1, "No turns", "one Chat() call, one complete response, judged after the fact", OK)
tier1_verbs = ["SURVEY_SPEC", "VERIFY_MANIFEST", "CERTIFY_MANIFEST", "DECOMPOSE_SPEC",
               "AUDIT_DECOMPOSITION", "RECONCILE_DECOMPOSITION", "ANALYZE_EXECUTION",
               "COMPRESS_ANALYSIS", "REVISE_PENDING"]
after = pack_chips(tier1_verbs, 0.85, y0 + 0.80, MAX_RIGHT, CHIP_H, 0.12, 0.10, OK, font_size=8.3)
flow_y = after + 0.22
ax.text(0.85, flow_y, "Chat()", fontsize=10, fontname=MONO_FONT, color=INK, ha="left", va="center", zorder=3)
ax.add_patch(FancyArrowPatch((1.42, flow_y), (2.05, flow_y), arrowstyle="-|>",
                              mutation_scale=10, linewidth=1.2, color=INK_DIM, zorder=3))
ax.text(2.12, flow_y, "Validate()", fontsize=10, fontname=MONO_FONT, color=INK, ha="left", va="center", zorder=3)
ax.text(3.55, flow_y, "one complete JSON response, no loop — mechanical checks decide valid / malformed",
         fontsize=9, style="italic", fontname=BODY_FONT, color=INK_DIM, ha="left", va="center", zorder=3)

# ---------------------------------------------------------------- Tier 2
y1 = y0 + band_h1 + 0.08
band_h2 = 1.55
band(y1, band_h2, 2, "Bounded completion-check loop", "ChatWithTools, looped — a yes/no question per turn, hard cap", WARN)
tier2_verbs = ["REFINE_TESTS_WRITE", "REFINE_TESTS_CRITIQUE", "REFINE_TESTS_JUDGE", "ADJUDICATE_NEXT_EXECUTION"]
after2 = pack_chips(tier2_verbs, 0.85, y1 + 0.80, MAX_RIGHT, CHIP_H, 0.15, 0.10, WARN, font_size=8.7)
ax.text(0.85, after2 + 0.22, "per turn: “tool called?” / “functions present?” — loops until yes, or stops at "
         "the cap (≈ 6 turns) regardless of outcome",
         fontsize=9, style="italic", fontname=BODY_FONT, color=INK_DIM, ha="left", va="top", zorder=3)

# ---------------------------------------------------------------- Tier 3
y2 = y1 + band_h2 + 0.08
band_h3 = 1.55
band(y2, band_h3, 3, "Stall / productivity supervision", "EXECUTE_BEAD only — wall-clock + content judged, external watchdog", STOP)
chip(0.85, y2 + 0.80, 2.05, CHIP_H, "EXECUTE_BEAD", STOP, font_size=9.5)
ax.text(3.15, y2 + 0.80 + CHIP_H / 2, "progressTracker: novel write_file content-hash -> productive turn",
         fontsize=9, style="italic", fontname=BODY_FONT, color=INK_DIM, ha="left", va="center", zorder=3)
ax.text(0.85, y2 + 0.80 + CHIP_H + 0.22,
         "12m checkpoint / 45m ceiling / content-stall timeout — plus an external MONITOR_EXECUTION "
         "watchdog subprocess that can kill it independently",
         fontsize=9, style="italic", fontname=BODY_FONT, color=INK_DIM, ha="left", va="top", zorder=3)

fig.savefig(OUT, facecolor=fig.get_facecolor())
plt.close(fig)
print("wrote", OUT)
