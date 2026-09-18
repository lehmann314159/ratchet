#!/usr/bin/env python3
"""Render the 4 FSM diagrams from docs/state-machine.pptx as standalone PNGs.

scripts/build-state-machine-pptx.py draws these as native PowerPoint
autoshapes (rounded rectangles + connectors, via deck_common.py's add_node /
add_edge) rather than embedded images, and this environment has no
LibreOffice/PowerPoint/Keynote to export a slide to an image. So this script
is a second, independent renderer (matplotlib, not python-pptx) that
reproduces the same node positions, edges, labels, and palette by hand for a
PNG-native fallback. Geometry here is copied from build-state-machine-pptx.py
diagram slides 3/5/7/12, not read from the .pptx file — the two are siblings,
not a single source pushed to two outputs. Regenerate this alongside that
script whenever its diagram calls change.

Font note: deck_common.py's BODY_FONT/MONO_FONT are Calibri/Consolas, chosen
for cross-platform pptx fidelity. Neither is installed as a system font here,
so this renderer substitutes Helvetica Neue / Menlo (both confirmed present
via matplotlib.font_manager) — same visual role, different exact face.
Georgia (the deck's TITLE_FONT) is installed natively and used as-is.

Run: pip install matplotlib   (any env; python-pptx not needed here)
     python3 scripts/build-state-machine-diagram-pngs.py
Output: docs/diagrams/deck/{1_project_status,2_bootstrap,3_bead_pipeline,4_job_status}.png
(a sibling to docs/diagrams/*.png, which are the Mermaid-rendered versions of
the same 4 diagrams used by docs/state-machine.pdf's build pipeline — these
are deck-styled instead, matching state-machine.pptx's own palette/layout.)
"""
import os
import matplotlib
matplotlib.use("Agg")
import matplotlib.pyplot as plt
from matplotlib.patches import FancyBboxPatch, FancyArrowPatch

OUT_DIR = os.path.join(os.path.dirname(__file__), "..", "docs", "diagrams", "deck")

# ---- palette (mirrors writing/decks/src/deck_common.py) ----
INK           = "#1C1E22"
INK_DIM       = "#5F636B"
GROUND        = "#F6F5F2"
SURFACE       = "#FFFFFF"
SURFACE2      = "#F1EFEA"
LINE          = "#E2E0DA"
ACCENT        = "#3D6D7E"
OK            = "#4F7A3F"
WARN          = "#B9781A"
STOP          = "#A83A2A"
TERMINAL_OK   = "#E7EFE1"
TERMINAL_STOP = "#F2E0DC"

TITLE_FONT = "Georgia"
BODY_FONT = "Helvetica Neue"   # substitute for Calibri
MONO_FONT = "Menlo"            # substitute for Consolas

SLIDE_W, SLIDE_H = 13.333, 7.5
DPI = 220
LABEL = "RATCHET · STATE MACHINE REFERENCE"

# Connection-site indices on a node, matching deck_common.py: 0=top, 1=right,
# 2=bottom, 3=left. _OUTWARD is each side's outward unit direction in
# data space (y grows downward, matching pptx / screen convention).
_OUTWARD = {0: (0, -1), 1: (1, 0), 2: (0, 1), 3: (-1, 0)}


class Diagram:
    def __init__(self, kicker, title, caption=None):
        self.fig, self.ax = plt.subplots(figsize=(SLIDE_W, SLIDE_H), dpi=DPI)
        ax = self.ax
        # Full-bleed axes: without this, matplotlib's default margins compress
        # the data coordinate system relative to the physical figure, so 1
        # data-unit no longer equals 1 inch — harmless here since every box
        # width below is a fixed literal, not derived from a text-width
        # measurement, but it's a latent trap for any future addition that
        # sizes a shape to fit measured text (see turn-eval-tiers.py, which
        # hit exactly this).
        self.fig.subplots_adjust(left=0, right=1, top=1, bottom=0)
        ax.set_xlim(0, SLIDE_W)
        ax.set_ylim(0, SLIDE_H)
        ax.invert_yaxis()  # pptx-style top-left origin, y grows downward
        ax.axis("off")
        self.fig.patch.set_facecolor(GROUND)
        ax.set_facecolor(GROUND)
        ax.text(0.7, 0.42, kicker.upper(), fontsize=11, fontweight="bold",
                 fontname=MONO_FONT, color=ACCENT, ha="left", va="top")
        ax.text(0.7, 0.95, title, fontsize=23, fontweight="bold",
                 fontname=TITLE_FONT, color=INK, ha="left", va="top")
        ax.plot([0.7, 12.63], [1.35, 1.35], color=LINE, linewidth=1)
        if caption:
            ax.text(0.7, 6.68, caption, fontsize=10.5, style="italic",
                     fontname=BODY_FONT, color=INK_DIM, ha="left", va="top",
                     wrap=True)
        ax.plot([0.7, 12.63], [7.0, 7.0], color=LINE, linewidth=1)
        ax.text(0.7, 7.15, LABEL, fontsize=8.5, fontname=MONO_FONT,
                 color=INK_DIM, ha="left", va="top")

    # ---- nodes ----
    def node(self, left, top, width, height, label, fill=SURFACE, border=ACCENT,
              text_color=INK, font_size=10.5):
        box = FancyBboxPatch((left, top), width, height,
                              boxstyle="round,pad=0,rounding_size=0.09",
                              linewidth=1.25, edgecolor=border, facecolor=fill,
                              zorder=2)
        self.ax.add_patch(box)
        self.ax.text(left + width / 2, top + height / 2, label,
                     fontsize=font_size, fontname=MONO_FONT, fontweight="bold",
                     color=text_color, ha="center", va="center", zorder=3)
        return dict(left=left, top=top, width=width, height=height)

    def _port(self, n, idx):
        pts = {0: (n["left"] + n["width"] / 2, n["top"]),
               1: (n["left"] + n["width"], n["top"] + n["height"] / 2),
               2: (n["left"] + n["width"] / 2, n["top"] + n["height"]),
               3: (n["left"], n["top"] + n["height"] / 2)}
        return pts[idx]

    def _route(self, p1, idx1, p2, idx2, clearance):
        """Orthogonal (elbow) path from p1 to p2, leaving/arriving along each
        port's outward direction. Approximates PowerPoint's elbow connector
        well enough for a readable diagram; not a pixel-identical replica."""
        d1, d2 = _OUTWARD[idx1], _OUTWARD[idx2]
        e1 = (p1[0] + d1[0] * clearance, p1[1] + d1[1] * clearance)
        e2 = (p2[0] + d2[0] * clearance, p2[1] + d2[1] * clearance)
        h1, h2 = d1[1] == 0, d2[1] == 0  # True if exit/entry is horizontal
        if h1 and h2:
            if abs(e1[1] - e2[1]) < 1e-6:
                mid = []
            else:
                midx = e1[0] if d1[0] * d2[0] < 0 else (e1[0] + e2[0]) / 2
                mid = [(midx, e1[1]), (midx, e2[1])]
        elif (not h1) and (not h2):
            if abs(e1[0] - e2[0]) < 1e-6:
                mid = []
            else:
                midy = e1[1] if d1[1] * d2[1] < 0 else (e1[1] + e2[1]) / 2
                mid = [(e1[0], midy), (e2[0], midy)]
        else:
            corner = (e2[0], e1[1]) if h1 else (e1[0], e2[1])
            mid = [corner]
        return [p1, e1] + mid + [e2, p2]

    def _arrowhead(self, a, b, color):
        arr = FancyArrowPatch(a, b, arrowstyle="-|>", mutation_scale=11,
                               linewidth=1.25, color=color, shrinkA=0, shrinkB=1,
                               zorder=2)
        self.ax.add_patch(arr)

    def edge(self, src, src_idx, dst, dst_idx, label=None, color=INK_DIM,
             elbow=True, label_size=10, label_nudge=(0, 0)):
        p1, p2 = self._port(src, src_idx), self._port(dst, dst_idx)
        if elbow:
            same_side_loop = src_idx == dst_idx
            clearance = 0.9 if same_side_loop else 0.4
            pts = self._route(p1, src_idx, p2, dst_idx, clearance)
            xs, ys = zip(*pts)
            self.ax.plot(xs, ys, color=color, linewidth=1.25,
                         solid_joinstyle="round", zorder=1)
            self._arrowhead(pts[-2], pts[-1], color)
        else:
            self.ax.plot([p1[0], p2[0]], [p1[1], p2[1]], color=color,
                         linewidth=1.25, zorder=1)
            self._arrowhead(p1, p2, color)
        if label:
            # Same midpoint rule as deck_common.add_edge: raw port midpoint,
            # nudged off the line only when both ports sit at the same
            # height (a same-row edge or loop, where the naive midpoint
            # would land on/inside a node) plus any manual nudge.
            dy = 0
            if p1[1] == p2[1]:
                if src_idx == 0 and dst_idx == 0:
                    dy = -0.85
                elif src_idx == 2 and dst_idx == 2:
                    dy = 0.85
                else:
                    dy = -0.5
            mx = (p1[0] + p2[0]) / 2 + label_nudge[0]
            my = (p1[1] + p2[1]) / 2 + dy + label_nudge[1]
            self.ax.text(mx, my, label, fontsize=label_size, style="italic",
                         fontname=BODY_FONT, color=color, ha="center", va="center",
                         zorder=4,
                         bbox=dict(boxstyle="square,pad=0.15", fc=GROUND, ec="none"))

    def edge_xy(self, x1, y1, x2, y2, label=None, color=INK_DIM, label_size=10):
        self.ax.plot([x1, x2], [y1, y2], color=color, linewidth=1.25, zorder=1)
        self._arrowhead((x1, y1), (x2, y2), color)
        if label:
            self.ax.text((x1 + x2) / 2, (y1 + y2) / 2, label, fontsize=label_size,
                         style="italic", fontname=BODY_FONT, color=color,
                         ha="center", va="center", zorder=4,
                         bbox=dict(boxstyle="square,pad=0.15", fc=GROUND, ec="none"))

    def save(self, path):
        self.fig.savefig(path, facecolor=self.fig.get_facecolor())
        plt.close(self.fig)


def build_project_status():
    d = Diagram("Diagram 1 of 4", "Project status",
        "projects.status CHECK IN ('active','full_stopped','complete','paused','fixture'). "
        "fixture and full_stopped/complete are terminal by design.")
    active = d.node(2.5, 3.85, 2.3, 0.75, "active")
    paused = d.node(2.5, 1.95, 2.3, 0.7, "paused", fill=SURFACE2, border=INK_DIM)
    complete = d.node(9.55, 2.4, 2.75, 0.75, "complete", fill=TERMINAL_OK, border=OK)
    full_stopped = d.node(9.55, 5.15, 2.75, 0.8, "full_stopped", fill=TERMINAL_STOP,
                           border=STOP, font_size=10)
    fixture = d.node(2.5, 5.65, 2.5, 0.75, "fixture", fill=SURFACE2, border=INK_DIM)
    d.edge_xy(0.4, 4.225, 2.5, 4.225, label="new-project / clone-project")
    d.edge(active, 0, paused, 2, label="pause knob <-> resume-project CLI", elbow=False)
    d.edge(active, 1, complete, 3, label="last bead succeeds", elbow=False)
    d.edge(active, 2, full_stopped, 0, label="recovery exhausted", elbow=True)
    d.edge(active, 3, fixture, 1, label="save-fixture CLI", elbow=False)
    d.save(os.path.join(OUT_DIR, "1_project_status.png"))


def build_bootstrap():
    d = Diagram("Diagram 2 of 4", "Bootstrap",
        "Runs once, before bead 1. All transitions automatic except the branch points "
        "marked with a verb's decision field.")
    survey = d.node(0.35, 3.6, 1.75, 0.8, "SURVEY_SPEC", font_size=10)
    verify = d.node(2.25, 3.6, 1.85, 0.8, "VERIFY_MANIFEST", font_size=10)
    certify = d.node(4.25, 3.6, 1.85, 0.8, "CERTIFY_MANIFEST", font_size=10)
    decompose = d.node(6.25, 3.6, 1.8, 0.8, "DECOMPOSE_SPEC", font_size=10)
    audit = d.node(8.2, 3.6, 2.05, 0.8, "AUDIT_DECOMPOSITION", font_size=9.5)
    approved = d.node(10.4, 3.6, 2.35, 0.8, "DECOMPOSITION_APPROVED", fill=TERMINAL_OK,
                       border=OK, font_size=9)
    reconcile = d.node(8.2, 5.55, 2.05, 0.8, "RECONCILE_DECOMPOSITION", font_size=8.5)
    d.edge_xy(0.35, 2.3, 1.225, 3.6, label="new-project (fresh)", label_size=9)
    d.edge_xy(9.4, 1.7, 9.22, 3.6, label="clone-project (cascade)", label_size=9)
    d.edge(survey, 1, verify, 3, elbow=False)
    d.edge(verify, 1, certify, 3, elbow=False)
    d.edge(certify, 1, decompose, 3, label="approve", elbow=False)
    d.edge(certify, 0, survey, 0, label="reject, count < 5", elbow=True)
    d.edge(decompose, 1, audit, 3, elbow=False)
    d.edge(audit, 1, approved, 3, label="no issues", elbow=False)
    d.edge(audit, 2, reconcile, 0, label="issues found", elbow=False)
    d.edge(reconcile, 3, audit, 3, label="disagree, round < cap (2)", elbow=True,
           label_nudge=(0, 0.35))
    d.edge(reconcile, 1, approved, 2, label="converged", elbow=True)
    d.save(os.path.join(OUT_DIR, "2_bootstrap.png"))


def build_bead_pipeline():
    d = Diagram("Diagram 3 of 4", "The per-bead pipeline",
        "beads.status: pending -> executing -> succeeded/full_stopped. Everything below "
        "is the verb chain inside 'executing.'")
    write = d.node(0.35, 3.6, 1.5, 0.8, "WRITE", font_size=10)
    critique = d.node(2.0, 3.6, 1.5, 0.8, "CRITIQUE", font_size=10)
    judge = d.node(3.65, 3.6, 1.35, 0.8, "JUDGE", font_size=10)
    execute = d.node(5.15, 3.6, 1.5, 0.8, "EXECUTE", font_size=10)
    analyze = d.node(6.8, 3.6, 1.5, 0.8, "ANALYZE", font_size=9.5)
    compress = d.node(8.45, 3.6, 1.6, 0.8, "COMPRESS", font_size=9.5)
    adjudicate = d.node(10.2, 3.6, 1.85, 0.8, "ADJUDICATE", font_size=9.5)
    done = d.node(9.7, 5.7, 1.3, 0.55, "done", fill=TERMINAL_OK, border=OK, font_size=9)
    stopped = d.node(11.2, 5.7, 1.9, 0.55, "full_stop", fill=TERMINAL_STOP, border=STOP,
                      font_size=9)
    d.edge(write, 1, critique, 3, label="compiles", elbow=False)
    d.edge(critique, 1, judge, 3, elbow=False)
    d.edge(judge, 0, write, 0, label="revise, cycle ≤ 5", elbow=True)
    d.edge(judge, 1, execute, 3, label="approved", elbow=False)
    d.edge(execute, 1, analyze, 3, elbow=False)
    d.edge(analyze, 1, compress, 3, elbow=False)
    d.edge(compress, 1, adjudicate, 3, elbow=False)
    d.edge(adjudicate, 0, execute, 0, label="retry: as-is/revised/test_reject", elbow=True,
           label_size=9)
    d.edge(adjudicate, 2, judge, 2, label="re_refine — bypasses cap", elbow=True,
           label_size=9)
    d.edge(adjudicate, 2, done, 0, label="declare_success, criteria pass", elbow=True,
           label_size=9)
    d.edge(adjudicate, 1, stopped, 0, label="full_stop", elbow=True)
    d.save(os.path.join(OUT_DIR, "3_bead_pipeline.png"))


def build_job_status():
    d = Diagram("Diagram 4 of 4", "Generic job status",
        "handoff_jobs.status, underneath every verb above except EXECUTE_BEAD (its own "
        "supervised loop, drawn in Diagram 3).")
    pending = d.node(1.4, 3.6, 2.0, 0.75, "pending")
    running = d.node(5.1, 3.6, 2.0, 0.75, "running")
    comp = d.node(8.8, 3.6, 2.2, 0.75, "complete", fill=TERMINAL_OK, border=OK)
    retry = d.node(5.1, 1.75, 2.0, 0.7, "failed_retry", fill=SURFACE2, border=WARN,
                    font_size=10)
    esc = d.node(8.8, 5.6, 2.2, 0.75, "escalated", fill=TERMINAL_STOP, border=STOP)
    d.edge(pending, 1, running, 3, label="claimNextJob (atomic)", elbow=False, label_size=9)
    d.edge(running, 1, comp, 3, label="Validate succeeds", elbow=False, label_size=9)
    d.edge(running, 0, retry, 2, label="Validate fails, strikes ≤ 2", elbow=True,
           label_size=9)
    d.edge(retry, 2, running, 0, label="reclaimed", elbow=True, label_nudge=(1.6, 0),
           label_size=9)
    d.edge(running, 2, esc, 0, label="strikes exceeded", elbow=True, label_size=9)
    d.save(os.path.join(OUT_DIR, "4_job_status.png"))


if __name__ == "__main__":
    os.makedirs(OUT_DIR, exist_ok=True)
    build_project_status()
    build_bootstrap()
    build_bead_pipeline()
    build_job_status()
    for f in sorted(os.listdir(OUT_DIR)):
        print("wrote", os.path.join(OUT_DIR, f))
