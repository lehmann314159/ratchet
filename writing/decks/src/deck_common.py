"""Shared visual system for the Ratchet deck series. Palette lifted from
scratchpad/burnin-dashboard.html for continuity with the other Ratchet
artifacts. Standalone-readable slides (no presenter) -> more self-contained
text per slide than a talk deck would carry.
"""
from pptx import Presentation
from pptx.util import Inches, Pt, Emu
from pptx.dml.color import RGBColor
from pptx.enum.text import PP_ALIGN, MSO_ANCHOR, MSO_AUTO_SIZE
from pptx.enum.shapes import MSO_SHAPE, MSO_CONNECTOR
from pptx.oxml.ns import qn
from pptx.oxml.xmlchemy import OxmlElement
import copy

# ---- palette (from burnin-dashboard.html :root) ----
INK      = RGBColor(0x1C, 0x1E, 0x22)
INK_DIM  = RGBColor(0x5F, 0x63, 0x6B)
GROUND   = RGBColor(0xF6, 0xF5, 0xF2)
SURFACE  = RGBColor(0xFF, 0xFF, 0xFF)
SURFACE2 = RGBColor(0xF1, 0xEF, 0xEA)
LINE     = RGBColor(0xE2, 0xE0, 0xDA)
ACCENT   = RGBColor(0x3D, 0x6D, 0x7E)
OK       = RGBColor(0x4F, 0x7A, 0x3F)
WARN     = RGBColor(0xB9, 0x78, 0x1A)
STOP     = RGBColor(0xA8, 0x3A, 0x2A)
WHITE    = RGBColor(0xFF, 0xFF, 0xFF)

TITLE_FONT = "Georgia"
BODY_FONT = "Calibri"
MONO_FONT = "Consolas"

SLIDE_W = Inches(13.333)
SLIDE_H = Inches(7.5)

SERIES = [
    "Ratchet",
    "Verbs and the Fleet",
    "Write for Zero Domain Knowledge",
    "The Burn-In",
]


def new_deck():
    prs = Presentation()
    prs.slide_width = SLIDE_W
    prs.slide_height = SLIDE_H
    return prs


def _blank_layout(prs):
    return prs.slide_layouts[6]  # blank


def add_slide(prs, bg=GROUND):
    slide = prs.slides.add_slide(_blank_layout(prs))
    rect = slide.shapes.add_shape(MSO_SHAPE.RECTANGLE, 0, 0, prs.slide_width, prs.slide_height)
    rect.fill.solid()
    rect.fill.fore_color.rgb = bg
    rect.line.fill.background()
    rect.shadow.inherit = False
    # send to back
    spTree = slide.shapes._spTree
    spTree.remove(rect._element)
    spTree.insert(2, rect._element)
    return slide


def _set_run(run, text, size=18, color=INK, font=BODY_FONT, bold=False, italic=False):
    run.text = text
    run.font.size = Pt(size)
    run.font.color.rgb = color
    run.font.name = font
    run.font.bold = bold
    run.font.italic = italic


def add_textbox(slide, left, top, width, height, anchor=MSO_ANCHOR.TOP, wrap=True):
    box = slide.shapes.add_textbox(left, top, width, height)
    tf = box.text_frame
    tf.word_wrap = wrap
    tf.auto_size = MSO_AUTO_SIZE.NONE
    tf.vertical_anchor = anchor
    tf.margin_left = 0
    tf.margin_right = 0
    tf.margin_top = 0
    tf.margin_bottom = 0
    return box, tf


def add_kicker(slide, text, left=Inches(0.7), top=Inches(0.42), width=Inches(11.9), color=ACCENT):
    box, tf = add_textbox(slide, left, top, width, Inches(0.35))
    p = tf.paragraphs[0]
    r = p.add_run()
    _set_run(r, text.upper(), size=13, color=color, font=MONO_FONT, bold=True)
    return box


def add_title(slide, text, left=Inches(0.7), top=Inches(0.72), width=Inches(11.9),
              size=34, color=INK, height=Inches(1.5)):
    box, tf = add_textbox(slide, left, top, width, height)
    p = tf.paragraphs[0]
    r = p.add_run()
    _set_run(r, text, size=size, color=color, font=TITLE_FONT, bold=True)
    p.line_spacing = 1.05
    return box


def add_rule(slide, top, left=Inches(0.7), width=Inches(11.9), color=LINE):
    ln = slide.shapes.add_connector(1, left, top, left + width, top)
    ln.line.color.rgb = color
    ln.line.width = Pt(1)
    return ln


def add_bullets(slide, items, left=Inches(0.7), top=Inches(2.0), width=Inches(11.9),
                 height=Inches(4.8), size=18, color=INK, gap=10, marker="—",
                 marker_color=None, bold_lead=False):
    """items: list of str, or (lead, rest) tuples for a bold-lead bullet."""
    box, tf = add_textbox(slide, left, top, width, height)
    marker_color = marker_color or ACCENT
    for i, item in enumerate(items):
        p = tf.paragraphs[0] if i == 0 else tf.add_paragraph()
        p.space_after = Pt(gap)
        p.line_spacing = 1.12
        rm = p.add_run()
        _set_run(rm, f"{marker}  ", size=size, color=marker_color, font=BODY_FONT, bold=True)
        if isinstance(item, tuple):
            lead, rest = item
            rl = p.add_run()
            _set_run(rl, lead, size=size, color=color, font=BODY_FONT, bold=True)
            rr = p.add_run()
            _set_run(rr, rest, size=size, color=color, font=BODY_FONT)
        else:
            rr = p.add_run()
            _set_run(rr, item, size=size, color=color, font=BODY_FONT)
    return box


def add_paragraph(slide, text, left=Inches(0.7), top=Inches(2.0), width=Inches(11.9),
                   height=Inches(3.0), size=19, color=INK, italic=False, align=PP_ALIGN.LEFT,
                   line_spacing=1.25, font=BODY_FONT):
    box, tf = add_textbox(slide, left, top, width, height)
    p = tf.paragraphs[0]
    p.alignment = align
    p.line_spacing = line_spacing
    r = p.add_run()
    _set_run(r, text, size=size, color=color, font=font, italic=italic)
    return box


def add_footer(slide, page, total, deck_label):
    box, tf = add_textbox(slide, Inches(0.7), Inches(7.08), Inches(11.9), Inches(0.32))
    p = tf.paragraphs[0]
    r = p.add_run()
    _set_run(r, f"{deck_label}", size=10, color=INK_DIM, font=MONO_FONT)
    p2 = tf.add_paragraph()  # unused spacer safeguard not needed
    tf.paragraphs[0].alignment = PP_ALIGN.LEFT
    # page number, right aligned, separate box
    box2, tf2 = add_textbox(slide, Inches(11.9), Inches(7.08), Inches(0.7), Inches(0.32))
    p3 = tf2.paragraphs[0]
    p3.alignment = PP_ALIGN.RIGHT
    r3 = p3.add_run()
    _set_run(r3, f"{page:02d} / {total:02d}", size=10, color=INK_DIM, font=MONO_FONT)
    add_rule(slide, Inches(7.0))
    return box


def add_gear(slide, left=Inches(11.85), top=Inches(0.5), size=Inches(0.5), color=ACCENT):
    box, tf = add_textbox(slide, left, top, size, size)
    p = tf.paragraphs[0]
    p.alignment = PP_ALIGN.RIGHT
    r = p.add_run()
    _set_run(r, "⚙", size=22, color=color, font="Segoe UI Symbol")
    return box


# ---------------------------------------------------------------- slide kinds

def title_slide(prs, deck_index, deck_title, subtitle, description, title_size=54):
    slide = add_slide(prs, bg=INK)
    add_kicker(slide, f"Ratchet · Part {deck_index} of 4", color=RGBColor(0x9A, 0xC7, 0xD3),
               top=Inches(2.35))
    box, tf = add_textbox(slide, Inches(0.9), Inches(2.85), Inches(11.5), Inches(1.15))
    p = tf.paragraphs[0]
    r = p.add_run()
    _set_run(r, deck_title, size=title_size, color=WHITE, font=TITLE_FONT, bold=True)
    p.line_spacing = 1.0
    box2, tf2 = add_textbox(slide, Inches(0.95), Inches(4.15), Inches(10.8), Inches(0.7))
    p2 = tf2.paragraphs[0]
    r2 = p2.add_run()
    _set_run(r2, subtitle, size=22, color=RGBColor(0xC9, 0xCD, 0xD2), font=BODY_FONT, italic=True)
    box3, tf3 = add_textbox(slide, Inches(0.95), Inches(5.15), Inches(9.6), Inches(1.4))
    p3 = tf3.paragraphs[0]
    p3.line_spacing = 1.3
    r3 = p3.add_run()
    _set_run(r3, description, size=15, color=RGBColor(0x9A, 0x9F, 0xA6), font=BODY_FONT)
    # series dots
    dot_y = Inches(6.85)
    for i in range(4):
        c = RGBColor(0x9A, 0xC7, 0xD3) if (i + 1) == deck_index else RGBColor(0x3A, 0x3E, 0x44)
        dot = slide.shapes.add_shape(MSO_SHAPE.OVAL, Inches(0.95 + i * 0.35), dot_y, Inches(0.16), Inches(0.16))
        dot.fill.solid()
        dot.fill.fore_color.rgb = c
        dot.line.fill.background()
        dot.shadow.inherit = False
    return slide


def section_slide(prs, kicker, title, note=None):
    slide = add_slide(prs, bg=ACCENT)
    add_kicker(slide, kicker, color=RGBColor(0xD8, 0xE7, 0xEA), top=Inches(2.9))
    box, tf = add_textbox(slide, Inches(0.9), Inches(3.35), Inches(11.5), Inches(1.8))
    p = tf.paragraphs[0]
    p.line_spacing = 1.05
    r = p.add_run()
    _set_run(r, title, size=44, color=WHITE, font=TITLE_FONT, bold=True)
    if note:
        box2, tf2 = add_textbox(slide, Inches(0.95), Inches(4.75), Inches(10.5), Inches(1.0))
        p2 = tf2.paragraphs[0]
        p2.line_spacing = 1.3
        r2 = p2.add_run()
        _set_run(r2, note, size=16, color=RGBColor(0xE3, 0xEC, 0xEE), font=BODY_FONT, italic=True)
    return slide


def content_slide(prs, page, total, deck_label, kicker, title, bullets=None,
                   title_size=32, bullet_size=18, top_bullets=Inches(2.15),
                   bullets_height=Inches(4.6)):
    slide = add_slide(prs)
    add_kicker(slide, kicker)
    add_title(slide, title, size=title_size, top=Inches(0.78))
    add_rule(slide, Inches(1.85))
    if bullets:
        add_bullets(slide, bullets, top=top_bullets, height=bullets_height, size=bullet_size)
    add_footer(slide, page, total, deck_label)
    return slide


def stat_slide(prs, page, total, deck_label, kicker, big, caption, sub=None, color=ACCENT):
    slide = add_slide(prs)
    add_kicker(slide, kicker, top=Inches(1.3))
    box, tf = add_textbox(slide, Inches(0.7), Inches(1.8), Inches(11.9), Inches(2.6), anchor=MSO_ANCHOR.MIDDLE)
    p = tf.paragraphs[0]
    r = p.add_run()
    _set_run(r, big, size=100, color=color, font=TITLE_FONT, bold=True)
    box2, tf2 = add_textbox(slide, Inches(0.75), Inches(4.5), Inches(11.5), Inches(0.8))
    p2 = tf2.paragraphs[0]
    r2 = p2.add_run()
    _set_run(r2, caption, size=24, color=INK, font=BODY_FONT, bold=True)
    if sub:
        box3, tf3 = add_textbox(slide, Inches(0.75), Inches(5.25), Inches(10.8), Inches(1.4))
        p3 = tf3.paragraphs[0]
        p3.line_spacing = 1.25
        r3 = p3.add_run()
        _set_run(r3, sub, size=16, color=INK_DIM, font=BODY_FONT)
    add_footer(slide, page, total, deck_label)
    return slide


def two_col_slide(prs, page, total, deck_label, kicker, title, left_head, left_items,
                   right_head, right_items, title_size=30):
    slide = add_slide(prs)
    add_kicker(slide, kicker)
    add_title(slide, title, size=title_size, top=Inches(0.78))
    add_rule(slide, Inches(1.85))
    colw = Inches(5.7)
    lbox, ltf = add_textbox(slide, Inches(0.7), Inches(2.15), colw, Inches(0.4))
    lp = ltf.paragraphs[0]
    lr = lp.add_run()
    _set_run(lr, left_head, size=15, color=ACCENT, font=MONO_FONT, bold=True)
    add_bullets(slide, left_items, left=Inches(0.7), top=Inches(2.65), width=colw, height=Inches(4.1), size=16)
    rbox, rtf = add_textbox(slide, Inches(6.9), Inches(2.15), colw, Inches(0.4))
    rp = rtf.paragraphs[0]
    rr = rp.add_run()
    _set_run(rr, right_head, size=15, color=ACCENT, font=MONO_FONT, bold=True)
    add_bullets(slide, right_items, left=Inches(6.9), top=Inches(2.65), width=colw, height=Inches(4.1), size=16)
    # divider
    ln = slide.shapes.add_connector(1, Inches(6.6), Inches(2.15), Inches(6.6), Inches(6.8))
    ln.line.color.rgb = LINE
    ln.line.width = Pt(1)
    add_footer(slide, page, total, deck_label)
    return slide


def table_slide(prs, page, total, deck_label, kicker, title, headers, rows,
                 col_widths=None, title_size=30, font_size=14, top=Inches(2.2)):
    slide = add_slide(prs)
    add_kicker(slide, kicker)
    add_title(slide, title, size=title_size, top=Inches(0.78))
    add_rule(slide, Inches(1.85))
    n_rows = len(rows) + 1
    n_cols = len(headers)
    tbl_w = Inches(11.9)
    tbl_h = Inches(min(4.6, 0.5 * n_rows))
    gshape = slide.shapes.add_table(n_rows, n_cols, Inches(0.7), top, tbl_w, tbl_h)
    table = gshape.table
    if col_widths:
        total_w = sum(col_widths)
        for i, w in enumerate(col_widths):
            table.columns[i].width = Emu(int(Inches(11.9) * (w / total_w)))
    for j, h in enumerate(headers):
        cell = table.cell(0, j)
        cell.text = ""
        tfc = cell.text_frame
        pc = tfc.paragraphs[0]
        rc = pc.add_run()
        _set_run(rc, h, size=font_size, color=WHITE, font=MONO_FONT, bold=True)
        cell.fill.solid()
        cell.fill.fore_color.rgb = INK
        cell.margin_left = Inches(0.08)
        cell.margin_top = Inches(0.04)
        cell.margin_bottom = Inches(0.04)
        cell.vertical_anchor = MSO_ANCHOR.MIDDLE
    for i, row in enumerate(rows):
        for j, val in enumerate(row):
            cell = table.cell(i + 1, j)
            cell.text = ""
            tfc = cell.text_frame
            tfc.word_wrap = True
            pc = tfc.paragraphs[0]
            rc = pc.add_run()
            bold0 = (j == 0)
            _set_run(rc, str(val), size=font_size, color=INK, font=BODY_FONT, bold=bold0)
            cell.fill.solid()
            cell.fill.fore_color.rgb = SURFACE if i % 2 == 0 else SURFACE2
            cell.margin_left = Inches(0.08)
            cell.margin_top = Inches(0.03)
            cell.margin_bottom = Inches(0.03)
            cell.vertical_anchor = MSO_ANCHOR.MIDDLE
    add_footer(slide, page, total, deck_label)
    return slide


def quote_slide(prs, page, total, deck_label, kicker, quote, context, quote_color=INK):
    slide = add_slide(prs, bg=SURFACE2)
    add_kicker(slide, kicker, top=Inches(1.0))
    bar = slide.shapes.add_shape(MSO_SHAPE.RECTANGLE, Inches(0.7), Inches(1.65), Inches(0.08), Inches(3.6))
    bar.fill.solid()
    bar.fill.fore_color.rgb = ACCENT
    bar.line.fill.background()
    bar.shadow.inherit = False
    box, tf = add_textbox(slide, Inches(1.05), Inches(1.6), Inches(10.9), Inches(3.7), anchor=MSO_ANCHOR.MIDDLE)
    p = tf.paragraphs[0]
    p.line_spacing = 1.3
    r = p.add_run()
    _set_run(r, quote, size=25, color=quote_color, font=TITLE_FONT, italic=True)
    box2, tf2 = add_textbox(slide, Inches(1.05), Inches(5.55), Inches(10.9), Inches(1.1))
    p2 = tf2.paragraphs[0]
    p2.line_spacing = 1.25
    r2 = p2.add_run()
    _set_run(r2, context, size=15, color=INK_DIM, font=BODY_FONT)
    add_footer(slide, page, total, deck_label)
    return slide


def closing_slide(prs, page, total, deck_label, headline, body, next_label=None):
    slide = add_slide(prs, bg=INK)
    add_kicker(slide, "END OF PART", color=RGBColor(0x9A, 0xC7, 0xD3), top=Inches(1.6))
    box, tf = add_textbox(slide, Inches(0.9), Inches(2.05), Inches(11.3), Inches(1.6))
    p = tf.paragraphs[0]
    p.line_spacing = 1.1
    r = p.add_run()
    _set_run(r, headline, size=36, color=WHITE, font=TITLE_FONT, bold=True)
    box2, tf2 = add_textbox(slide, Inches(0.95), Inches(3.55), Inches(10.6), Inches(2.1))
    p2 = tf2.paragraphs[0]
    p2.line_spacing = 1.3
    r2 = p2.add_run()
    _set_run(r2, body, size=17, color=RGBColor(0xC9, 0xCD, 0xD2), font=BODY_FONT)
    if next_label:
        box3, tf3 = add_textbox(slide, Inches(0.95), Inches(6.0), Inches(10.6), Inches(0.6))
        p3 = tf3.paragraphs[0]
        r3a = p3.add_run()
        _set_run(r3a, "NEXT — ", size=14, color=RGBColor(0x9A, 0xC7, 0xD3), font=MONO_FONT, bold=True)
        r3b = p3.add_run()
        _set_run(r3b, next_label, size=16, color=WHITE, font=BODY_FONT, italic=True)
    add_footer_dark(slide, page, total, deck_label)
    return slide


def add_footer_dark(slide, page, total, deck_label):
    box, tf = add_textbox(slide, Inches(0.7), Inches(7.08), Inches(11.9), Inches(0.32))
    p = tf.paragraphs[0]
    r = p.add_run()
    _set_run(r, deck_label, size=10, color=RGBColor(0x6B, 0x70, 0x77), font=MONO_FONT)
    box2, tf2 = add_textbox(slide, Inches(11.9), Inches(7.08), Inches(0.7), Inches(0.32))
    p3 = tf2.paragraphs[0]
    p3.alignment = PP_ALIGN.RIGHT
    r3 = p3.add_run()
    _set_run(r3, f"{page:02d} / {total:02d}", size=10, color=RGBColor(0x6B, 0x70, 0x77), font=MONO_FONT)


# ---------------------------------------------------------------- FSM diagram primitives
# Connection-site indices on a rectangle-family autoshape: 0=top, 1=right, 2=bottom, 3=left.

TERMINAL_OK = RGBColor(0xE7, 0xEF, 0xE1)
TERMINAL_STOP = RGBColor(0xF2, 0xE0, 0xDC)

def add_node(slide, left, top, width, height, label, fill=SURFACE, border=ACCENT,
             text_color=INK, font_size=10.5, bold=True):
    box = slide.shapes.add_shape(MSO_SHAPE.ROUNDED_RECTANGLE, left, top, width, height)
    box.fill.solid()
    box.fill.fore_color.rgb = fill
    box.line.color.rgb = border
    box.line.width = Pt(1.25)
    box.shadow.inherit = False
    tf = box.text_frame
    tf.word_wrap = True
    tf.auto_size = MSO_AUTO_SIZE.NONE
    tf.margin_left = Pt(3); tf.margin_right = Pt(3)
    tf.margin_top = Pt(1); tf.margin_bottom = Pt(1)
    tf.vertical_anchor = MSO_ANCHOR.MIDDLE
    p = tf.paragraphs[0]
    p.alignment = PP_ALIGN.CENTER
    p.line_spacing = 0.95
    r = p.add_run()
    _set_run(r, label, size=font_size, color=text_color, font=MONO_FONT, bold=bold)
    return box


def _arrowhead(line_format, kind="tailEnd", shape="triangle"):
    ln = line_format._get_or_add_ln()
    el = OxmlElement(f"a:{kind}")
    el.set("type", shape)
    ln.append(el)


def _dash(line_format):
    ln = line_format._get_or_add_ln()
    pd = OxmlElement("a:prstDash")
    pd.set("val", "dash")
    ln.append(pd)


def _label_at(slide, x, y, text, color=INK_DIM, size=10, bg=None, width=None):
    w = width if width is not None else Inches(min(2.3, max(0.85, 0.082 * len(text) + 0.28)))
    h = Inches(0.3)
    left, top = x - w // 2, y - h // 2
    if bg is not None:
        plate = slide.shapes.add_shape(MSO_SHAPE.RECTANGLE, left, top, w, h)
        plate.fill.solid()
        plate.fill.fore_color.rgb = bg
        plate.line.fill.background()
        plate.shadow.inherit = False
    box, tf = add_textbox(slide, left, top, w, h, anchor=MSO_ANCHOR.MIDDLE)
    p = tf.paragraphs[0]
    p.alignment = PP_ALIGN.CENTER
    r = p.add_run()
    _set_run(r, text, size=size, color=color, font=BODY_FONT, italic=True)


def add_edge(slide, src, src_idx, dst, dst_idx, label=None, color=INK_DIM,
             elbow=True, dashed=False, width_pt=1.25, label_size=10, label_bg=GROUND,
             label_nudge=(0, 0)):
    kind = MSO_CONNECTOR.ELBOW if elbow else MSO_CONNECTOR.STRAIGHT
    conn = slide.shapes.add_connector(kind, src.left, src.top, dst.left, dst.top)
    conn.begin_connect(src, src_idx)
    conn.end_connect(dst, dst_idx)
    conn.line.color.rgb = color
    conn.line.width = Pt(width_pt)
    if dashed:
        _dash(conn.line)
    _arrowhead(conn.line)
    if label:
        pts = {0: (src.left + src.width // 2, src.top),
               1: (src.left + src.width, src.top + src.height // 2),
               2: (src.left + src.width // 2, src.top + src.height),
               3: (src.left, src.top + src.height // 2)}
        ptd = {0: (dst.left + dst.width // 2, dst.top),
               1: (dst.left + dst.width, dst.top + dst.height // 2),
               2: (dst.left + dst.width // 2, dst.top + dst.height),
               3: (dst.left, dst.top + dst.height // 2)}
        x1, y1 = pts[src_idx]
        x2, y2 = ptd[dst_idx]
        # A raw midpoint between two same-height connection points lands inside
        # (or flush against) whichever box(es) sit at that height — nudge such
        # labels off the row/loop line they sit on. Endpoints at different
        # heights already have natural clearance in the gap between them.
        dy = 0
        if y1 == y2:
            if src_idx == 0 and dst_idx == 0:
                dy = -Inches(1.0)   # loop arcing above the row: clear the row-label band too
            elif src_idx == 2 and dst_idx == 2:
                dy = Inches(1.0)    # loop arcing below the row
            else:
                dy = -Inches(0.6)   # adjacent same-row edge: lift just above the row
        ndx, ndy = label_nudge
        _label_at(slide, (x1 + x2) // 2 + Inches(ndx), (y1 + y2) // 2 + dy + Inches(ndy),
                  label, color=color, size=label_size, bg=label_bg)
    return conn


def add_edge_xy(slide, x1, y1, x2, y2, label=None, color=INK_DIM, dashed=False,
                 width_pt=1.25, label_size=10, label_bg=GROUND):
    conn = slide.shapes.add_connector(MSO_CONNECTOR.STRAIGHT, x1, y1, x2, y2)
    conn.line.color.rgb = color
    conn.line.width = Pt(width_pt)
    if dashed:
        _dash(conn.line)
    _arrowhead(conn.line)
    if label:
        _label_at(slide, (x1 + x2) // 2, (y1 + y2) // 2, label, color=color,
                  size=label_size, bg=label_bg)
    return conn


def diagram_header(prs, page, total, deck_label, kicker, title, caption=None):
    slide = add_slide(prs)
    add_kicker(slide, kicker)
    add_title(slide, title, size=26, top=Inches(0.7), height=Inches(0.55))
    add_rule(slide, Inches(1.35))
    if caption:
        box, tf = add_textbox(slide, Inches(0.7), Inches(6.62), Inches(11.9), Inches(0.4))
        p = tf.paragraphs[0]
        r = p.add_run()
        _set_run(r, caption, size=12, color=INK_DIM, font=BODY_FONT, italic=True)
    add_footer(slide, page, total, deck_label)
    return slide
