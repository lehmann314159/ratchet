#!/usr/bin/env bash
# Build docs/state-machine.pdf from docs/ratchet_state_machine.md with the
# Mermaid diagrams rendered and inlined.
#
#   1. extract each ```mermaid block -> diagrams/N_<slug>.mmd
#   2. render each to diagrams/N_<slug>.png via mermaid-cli (-b white -s 3)
#   3. strip the ```mermaid blocks from a working copy of the .md
#      (the ![](diagrams/*.png) tags right after each block stay)
#   4. pandoc -> standalone HTML with images embedded
#   5. headless Chrome -> PDF
#
# Deps: npx (mermaid-cli via npx), pandoc, Google Chrome.
set -euo pipefail

cd "$(dirname "$0")/.."
DOC="docs/ratchet_state_machine.md"
DIA="docs/diagrams"
OUT="docs/state-machine.pdf"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

CHROME="${CHROME:-/Applications/Google Chrome.app/Contents/MacOS/Google Chrome}"
MMDC_VERSION="11.16.0"

# puppeteer needs this on some macOS setups; harmless otherwise.
cat > "$TMP/puppeteer.json" <<'JSON'
{ "args": ["--no-sandbox"] }
JSON

echo ">> extracting + rendering mermaid blocks"
python3 - "$DOC" "$DIA" "$TMP" <<'PY'
import re, sys, pathlib
doc, dia, tmp = sys.argv[1], sys.argv[2], sys.argv[3]
text = pathlib.Path(doc).read_text()
blocks = re.findall(r"```mermaid\n(.*?)\n```", text, re.S)
# slugs must match the ![](diagrams/<file>.png) names already in the doc
imgs = re.findall(r"\]\(diagrams/([0-9]+_[a-z_]+)\.png\)", text)
assert len(blocks) == len(imgs), f"{len(blocks)} mermaid blocks vs {len(imgs)} image tags"
for src, slug in zip(blocks, imgs):
    p = pathlib.Path(tmp) / f"{slug}.mmd"
    p.write_text(src + "\n")
    print(slug)
PY

for mmd in "$TMP"/*.mmd; do
  slug="$(basename "$mmd" .mmd)"
  echo "   render $slug"
  npx --yes "@mermaid-js/mermaid-cli@${MMDC_VERSION}" \
    -i "$mmd" -o "$DIA/${slug}.png" -b white -s 3 \
    -p "$TMP/puppeteer.json" >/dev/null
done

echo ">> stripping mermaid source blocks for the PDF copy"
python3 - "$DOC" "$TMP/doc.md" <<'PY'
import re, sys, pathlib
src, dst = sys.argv[1], sys.argv[2]
text = pathlib.Path(src).read_text()
text = re.sub(r"```mermaid\n.*?\n```\n\n?", "", text, flags=re.S)
pathlib.Path(dst).write_text(text)
PY

echo ">> pandoc -> html"
cat > "$TMP/style.css" <<'CSS'
@page { size: A4; margin: 18mm 16mm; }
html { font-size: 11pt; }
body { font-family: -apple-system, "Helvetica Neue", Arial, sans-serif;
       line-height: 1.45; color: #1a1a1a; max-width: none; }
h1 { font-size: 1.9rem; border-bottom: 2px solid #333; padding-bottom: .2em; }
h2 { font-size: 1.4rem; margin-top: 1.6em; border-bottom: 1px solid #ccc;
     padding-bottom: .15em; page-break-after: avoid; }
h3 { font-size: 1.1rem; page-break-after: avoid; }
code { background: #f2f2f2; padding: .1em .3em; border-radius: 3px;
       font-size: .88em; font-family: "SF Mono", ui-monospace, Menlo, monospace; }
pre code { display: block; padding: .8em; overflow-x: auto; }
img { max-width: 100%; height: auto; display: block; margin: 1em auto;
      border: 1px solid #e0e0e0; page-break-inside: avoid; }
table { border-collapse: collapse; width: 100%; font-size: .9rem;
        page-break-inside: avoid; }
th, td { border: 1px solid #ccc; padding: .35em .55em; text-align: left;
         vertical-align: top; }
th { background: #f2f2f2; }
blockquote { border-left: 3px solid #bbb; margin-left: 0; padding-left: 1em;
             color: #444; }
CSS

pandoc "$TMP/doc.md" \
  --standalone --embed-resources \
  --metadata title="Ratchet State Machine" \
  --resource-path="docs" \
  --css "$TMP/style.css" \
  -o "$TMP/doc.html"

echo ">> chrome -> pdf"
"$CHROME" --headless --disable-gpu --no-pdf-header-footer \
  --print-to-pdf="$OUT" "file://$TMP/doc.html" 2>/dev/null

echo ">> wrote $OUT"
ls -la "$OUT"
