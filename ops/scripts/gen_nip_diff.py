#!/usr/bin/env python3
"""Generate a local HTML page for reading or diffing a doc (e.g. docs/nips/*.md).

Both modes render the file as a real markdown preview — headings, tables,
lists, code blocks — never a raw-source dump, and always the complete
content: nothing is ever collapsed, truncated, or hidden behind a click.

  diff mode (--old and --new both given): the rendered preview of --new,
  with a block-level diff against --old overlaid on top of it — a changed
  paragraph/heading shows word-level <del>/<ins> highlighting inline, a
  wholly removed block renders (in full) with a red tint, a wholly added
  block renders (in full) with a green tint. Unchanged blocks render plain.

  read mode (--old omitted): the full current content of --new, rendered,
  no diff markup at all.

Writes one HTML page per run into an output directory and rebuilds that
directory's index.html from its manifest.json.

This is dev tooling for reviewing/reading prose/spec docs before they land —
not part of the build. Output is regenerated on demand and is not meant to
be committed; see docs/artifacts/ in .gitignore.

Usage:
    # diff mode — rendered preview with an overlaid block/word diff
    python3 ops/scripts/gen_nip_diff.py \
        --old /path/to/before.md --new docs/nips/NIP-CASH.md \
        --title "NIP-CASH.md" --slug nip-cash \
        --summary "Trimmed three repeated rationale passages." \
        --out-dir docs/artifacts

    # read mode — rendered preview of the full current content, no diff
    python3 ops/scripts/gen_nip_diff.py \
        --new docs/nips/NIP-CASH.md \
        --title "NIP-CASH.md" --slug nip-cash-read \
        --summary "Full current text." \
        --out-dir docs/artifacts
"""
import argparse
import difflib
import html
import json
import pathlib
import re
from datetime import datetime, timezone

WORD_RE = re.compile(r"\s+|\S+")

# Sentinels used to carry diff boundaries through inline markdown rendering
# (bold/italic/code/link parsing) without being mistaken for real markup.
# Control characters: never appear in real prose, untouched by html.escape.
DEL_OPEN, DEL_CLOSE = "\x01", "\x02"
INS_OPEN, INS_CLOSE = "\x03", "\x04"


def word_tokenize(text: str) -> list[str]:
    return WORD_RE.findall(text)


# ---------------------------------------------------------------------------
# inline rendering — markdown -> HTML for a single logical run of text
# ---------------------------------------------------------------------------

INLINE_CODE_RE = re.compile(r"`([^`]+)`")
LINK_RE = re.compile(r"\[([^\]]+)\]\(([^)]+)\)")
BOLD_RE = re.compile(r"\*\*(.+?)\*\*")
ITALIC_RE = re.compile(r"(?<!\*)\*(?!\s)([^*\n]+?)(?<!\s)\*(?!\*)")


def render_inline(text: str) -> str:
    """Inline markdown -> HTML: code spans, bold, italic, links. Escapes
    everything else. Code spans are protected so their contents never get
    re-interpreted as bold/italic markup. Diff sentinels (if present) pass
    through untouched — the caller swaps them for <del>/<ins> afterward."""
    escaped = html.escape(text)

    stashed = []

    def stash_code(m):
        stashed.append(f"<code>{m.group(1)}</code>")
        return f"\x00{len(stashed) - 1}\x00"

    escaped = INLINE_CODE_RE.sub(stash_code, escaped)
    escaped = LINK_RE.sub(lambda m: f'<a href="{m.group(2)}">{m.group(1)}</a>', escaped)
    escaped = BOLD_RE.sub(lambda m: f"<strong>{m.group(1)}</strong>", escaped)
    escaped = ITALIC_RE.sub(lambda m: f"<em>{m.group(1)}</em>", escaped)
    escaped = re.sub(r"\x00(\d+)\x00", lambda m: stashed[int(m.group(1))], escaped)
    return escaped


def render_inline_diff(old_text: str, new_text: str) -> tuple[str, int, int]:
    """Word-level diff of two logical text runs, rendered through the same
    inline markdown pipeline as render_inline. Returns (html, added, removed)
    word counts."""
    old_tokens = word_tokenize(old_text)
    new_tokens = word_tokenize(new_text)
    sm = difflib.SequenceMatcher(None, old_tokens, new_tokens, autojunk=False)
    hybrid = []
    added = removed = 0
    for tag, i1, i2, j1, j2 in sm.get_opcodes():
        if tag == "equal":
            hybrid.append("".join(old_tokens[i1:i2]))
        elif tag == "delete":
            removed += i2 - i1
            hybrid.append(DEL_OPEN + "".join(old_tokens[i1:i2]) + DEL_CLOSE)
        elif tag == "insert":
            added += j2 - j1
            hybrid.append(INS_OPEN + "".join(new_tokens[j1:j2]) + INS_CLOSE)
        elif tag == "replace":
            removed += i2 - i1
            added += j2 - j1
            hybrid.append(DEL_OPEN + "".join(old_tokens[i1:i2]) + DEL_CLOSE)
            hybrid.append(INS_OPEN + "".join(new_tokens[j1:j2]) + INS_CLOSE)
    rendered = render_inline("".join(hybrid))
    rendered = (
        rendered.replace(DEL_OPEN, "<del>")
        .replace(DEL_CLOSE, "</del>")
        .replace(INS_OPEN, "<ins>")
        .replace(INS_CLOSE, "</ins>")
    )
    return rendered, added, removed


# ---------------------------------------------------------------------------
# block-level parsing — markdown source -> a list of block records
# ---------------------------------------------------------------------------

TABLE_SEP_RE = re.compile(r"^\s*\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)*\|?\s*$")
FENCE_RE = re.compile(r"^```(\w*)\s*$")
ATX_RE = re.compile(r"^(#{1,6})\s+(.*)$")
SETEXT_H1_RE = re.compile(r"^=+\s*$")
SETEXT_H2_RE = re.compile(r"^-{3,}\s*$")
HR_RE = re.compile(r"^(-{3,}|_{3,}|\*{3,})\s*$")
UL_RE = re.compile(r"^\s*-\s+(.*)$")
OL_RE = re.compile(r"^\s*\d+\.\s+(.*)$")
LIST_MARKER_RE = re.compile(r"^\s*(-\s+|\d+\.\s+)")
CONTINUATION_RE = re.compile(r"^\s+\S")


def split_table_row(line: str) -> list[str]:
    line = line.strip()
    if line.startswith("|"):
        line = line[1:]
    if line.endswith("|"):
        line = line[:-1]
    return [c.strip() for c in line.split("|")]


def parse_blocks(text: str) -> list[dict]:
    """Parse markdown source into block records. Each block has at least
    `kind` and `raw` (a stable string signature used for block-level
    diffing); type-specific fields hold what's needed to render it."""
    lines = text.split("\n")
    n = len(lines)
    blocks: list[dict] = []
    paragraph_buf: list[str] = []
    i = 0

    def flush_paragraph():
        if paragraph_buf:
            joined = " ".join(l.strip() for l in paragraph_buf)
            blocks.append({"kind": "p", "text": joined, "raw": joined})
            paragraph_buf.clear()

    while i < n:
        line = lines[i]

        fence = FENCE_RE.match(line)
        if fence:
            flush_paragraph()
            lang = fence.group(1)
            i += 1
            code_lines = []
            while i < n and not re.match(r"^```\s*$", lines[i]):
                code_lines.append(lines[i])
                i += 1
            i += 1  # skip closing fence
            code = "\n".join(code_lines)
            blocks.append({"kind": "code", "lang": lang, "code": code, "raw": f"```{lang}\n{code}\n```"})
            continue

        if line.strip() == "":
            flush_paragraph()
            i += 1
            continue

        if not paragraph_buf and i + 1 < n and line.strip():
            nxt = lines[i + 1]
            if SETEXT_H1_RE.match(nxt):
                t = line.strip()
                blocks.append({"kind": "h1", "text": t, "raw": t})
                i += 2
                continue
            if SETEXT_H2_RE.match(nxt) and not HR_RE.match(line):
                t = line.strip()
                blocks.append({"kind": "h2", "text": t, "raw": t})
                i += 2
                continue

        atx = ATX_RE.match(line)
        if atx:
            flush_paragraph()
            level = len(atx.group(1))
            t = atx.group(2).strip()
            blocks.append({"kind": f"h{level}", "text": t, "raw": t})
            i += 1
            continue

        if HR_RE.match(line):
            flush_paragraph()
            blocks.append({"kind": "hr", "raw": "---"})
            i += 1
            continue

        if "|" in line and i + 1 < n and TABLE_SEP_RE.match(lines[i + 1]):
            flush_paragraph()
            header = split_table_row(line)
            i += 2
            body_rows = []
            while i < n and "|" in lines[i] and lines[i].strip():
                body_rows.append(split_table_row(lines[i]))
                i += 1
            raw = "\n".join(["|".join(header)] + ["|".join(r) for r in body_rows])
            blocks.append({"kind": "table", "header": header, "rows": body_rows, "raw": raw})
            continue

        if UL_RE.match(line) or OL_RE.match(line):
            flush_paragraph()
            is_ordered = OL_RE.match(line) is not None
            items = []
            while i < n:
                m = OL_RE.match(lines[i]) if is_ordered else UL_RE.match(lines[i])
                if not m:
                    break
                item_lines = [m.group(1)]
                i += 1
                while (
                    i < n
                    and lines[i].strip() != ""
                    and CONTINUATION_RE.match(lines[i])
                    and not LIST_MARKER_RE.match(lines[i])
                ):
                    item_lines.append(lines[i].strip())
                    i += 1
                items.append(" ".join(item_lines))
            blocks.append({
                "kind": "ol" if is_ordered else "ul",
                "items": items,
                "raw": "\n".join(items),
            })
            continue

        paragraph_buf.append(line)
        i += 1

    flush_paragraph()
    return blocks


HEADING_KINDS = {"h1", "h2", "h3", "h4", "h5", "h6"}


def render_block(b: dict) -> str:
    kind = b["kind"]
    if kind in HEADING_KINDS:
        return f"<{kind}>{render_inline(b['text'])}</{kind}>"
    if kind == "p":
        return f"<p>{render_inline(b['text'])}</p>"
    if kind == "hr":
        return "<hr>"
    if kind == "code":
        if b["lang"] == "mermaid":
            return f'<pre class="mermaid">{html.escape(b["code"])}</pre>'
        cls = f' class="language-{b["lang"]}"' if b["lang"] else ""
        return f"<pre><code{cls}>{html.escape(b['code'])}</code></pre>"
    if kind == "table":
        thead = "".join(f"<th>{render_inline(c)}</th>" for c in b["header"])
        tbody = "".join(
            "<tr>" + "".join(f"<td>{render_inline(c)}</td>" for c in r) + "</tr>"
            for r in b["rows"]
        )
        return f"<table><thead><tr>{thead}</tr></thead><tbody>{tbody}</tbody></table>"
    if kind in ("ul", "ol"):
        lis = "".join(f"<li>{render_inline(it)}</li>" for it in b["items"])
        return f"<{kind}>{lis}</{kind}>"
    raise ValueError(f"unknown block kind {kind!r}")


def render_document(text: str) -> str:
    return "\n".join(render_block(b) for b in parse_blocks(text))


# ---------------------------------------------------------------------------
# block-level diff rendering — same preview, with changes overlaid in place
# ---------------------------------------------------------------------------

TEXTUAL_KINDS = HEADING_KINDS | {"p"}


def render_list_pair_diff(ob: dict, nb: dict) -> tuple[str, int, int]:
    """Item-level diff between two same-kind (ul/ol) blocks — a changed
    bullet gets word-level highlighting in place, rather than the whole
    list showing as one wholesale removal+addition."""
    added_words = removed_words = 0
    li_parts = []
    sm = difflib.SequenceMatcher(None, ob["items"], nb["items"], autojunk=False)
    for tag, i1, i2, j1, j2 in sm.get_opcodes():
        if tag == "equal":
            for k in range(i2 - i1):
                li_parts.append(f"<li>{render_inline(nb['items'][j1 + k])}</li>")
        elif tag == "delete":
            for k in range(i1, i2):
                removed_words += len(word_tokenize(ob["items"][k]))
                li_parts.append(f'<li class="diff-li removed">{render_inline(ob["items"][k])}</li>')
        elif tag == "insert":
            for k in range(j1, j2):
                added_words += len(word_tokenize(nb["items"][k]))
                li_parts.append(f'<li class="diff-li added">{render_inline(nb["items"][k])}</li>')
        elif tag == "replace":
            old_items = ob["items"][i1:i2]
            new_items = nb["items"][j1:j2]
            pair_n = min(len(old_items), len(new_items))
            for k in range(pair_n):
                inner, a, r = render_inline_diff(old_items[k], new_items[k])
                added_words += a
                removed_words += r
                li_parts.append(f"<li>{inner}</li>")
            for k in range(pair_n, len(old_items)):
                removed_words += len(word_tokenize(old_items[k]))
                li_parts.append(f'<li class="diff-li removed">{render_inline(old_items[k])}</li>')
            for k in range(pair_n, len(new_items)):
                added_words += len(word_tokenize(new_items[k]))
                li_parts.append(f'<li class="diff-li added">{render_inline(new_items[k])}</li>')
    return f"<{nb['kind']}>{''.join(li_parts)}</{nb['kind']}>", added_words, removed_words


def render_document_diff(old_text: str, new_text: str) -> tuple[str, int, int]:
    old_blocks = parse_blocks(old_text)
    new_blocks = parse_blocks(new_text)
    old_sigs = [b["raw"] for b in old_blocks]
    new_sigs = [b["raw"] for b in new_blocks]
    sm = difflib.SequenceMatcher(None, old_sigs, new_sigs, autojunk=False)

    parts = []
    added_words = removed_words = 0

    def wrap(html_str: str, cls: str) -> str:
        return f'<div class="diff-block {cls}">{html_str}</div>'

    for tag, i1, i2, j1, j2 in sm.get_opcodes():
        if tag == "equal":
            for k in range(i2 - i1):
                parts.append(render_block(new_blocks[j1 + k]))
        elif tag == "delete":
            for k in range(i1, i2):
                removed_words += len(word_tokenize(old_blocks[k]["raw"]))
                parts.append(wrap(render_block(old_blocks[k]), "removed"))
        elif tag == "insert":
            for k in range(j1, j2):
                added_words += len(word_tokenize(new_blocks[k]["raw"]))
                parts.append(wrap(render_block(new_blocks[k]), "added"))
        elif tag == "replace":
            old_slice = old_blocks[i1:i2]
            new_slice = new_blocks[j1:j2]
            pair_n = min(len(old_slice), len(new_slice))
            for k in range(pair_n):
                ob, nb = old_slice[k], new_slice[k]
                if ob["kind"] == nb["kind"] and ob["kind"] in TEXTUAL_KINDS:
                    inner, a, r = render_inline_diff(ob["text"], nb["text"])
                    added_words += a
                    removed_words += r
                    parts.append(f"<{nb['kind']}>{inner}</{nb['kind']}>" if nb["kind"] != "p" else f"<p>{inner}</p>")
                elif ob["kind"] == nb["kind"] and ob["kind"] in ("ul", "ol"):
                    list_html, a, r = render_list_pair_diff(ob, nb)
                    added_words += a
                    removed_words += r
                    parts.append(list_html)
                else:
                    removed_words += len(word_tokenize(ob["raw"]))
                    added_words += len(word_tokenize(nb["raw"]))
                    parts.append(wrap(render_block(ob), "removed"))
                    parts.append(wrap(render_block(nb), "added"))
            for k in range(pair_n, len(old_slice)):
                removed_words += len(word_tokenize(old_slice[k]["raw"]))
                parts.append(wrap(render_block(old_slice[k]), "removed"))
            for k in range(pair_n, len(new_slice)):
                added_words += len(word_tokenize(new_slice[k]["raw"]))
                parts.append(wrap(render_block(new_slice[k]), "added"))

    return "\n".join(parts), added_words, removed_words


# ---------------------------------------------------------------------------
# page templates
# ---------------------------------------------------------------------------

PAGE_CSS = """
:root {
  --bg: #f7f4ee; --surface: #ffffff; --ink: #211d16; --ink-muted: #6b6355;
  --rule: #ddd4c0; --accent: #a13a1f; --code-bg: #efe9db;
  --del-bg: #fbe4e1; --del-text: #8a2318; --ins-bg: #e1f2e2; --ins-text: #1f6b2e;
}
@media (prefers-color-scheme: dark) {
  :root {
    --bg: #16130e; --surface: #1e1a13; --ink: #ece5d6; --ink-muted: #a89d87;
    --rule: #332c20; --accent: #e17e56; --code-bg: #241f16;
    --del-bg: #3a1c17; --del-text: #ff8f7d; --ins-bg: #15321a; --ins-text: #7fd98f;
  }
}
* { box-sizing: border-box; }
body {
  margin: 0; background: var(--bg); color: var(--ink);
  font-family: "Iowan Old Style", "Palatino Linotype", Palatino, Georgia, serif;
  line-height: 1.65;
}
.page { max-width: 46rem; margin: 0 auto; padding: 2.5rem 1.5rem 5rem; }
a.back {
  font-family: ui-monospace, "SF Mono", Menlo, Consolas, monospace;
  color: var(--ink-muted); text-decoration: none; font-size: 0.82rem;
}
a.back:hover { color: var(--ink); }
.meta {
  font-family: ui-monospace, "SF Mono", Menlo, Consolas, monospace;
  color: var(--ink-muted); font-size: 0.82rem; margin: 0.5rem 0 2rem;
}
.meta .added { color: var(--ins-text); font-weight: 700; }
.meta .removed { color: var(--del-text); font-weight: 700; }
article h1 { font-size: 2rem; margin: 1.4rem 0 0.3rem; line-height: 1.1; }
article h2 {
  font-size: 1.3rem; margin: 2.4rem 0 0.9rem; padding-bottom: 0.4rem;
  border-bottom: 1px solid var(--rule);
}
article h3 { font-size: 1.05rem; margin: 1.8rem 0 0.6rem; }
article h4, article h5, article h6 { font-size: 1rem; margin: 1.4rem 0 0.5rem; }
article p { margin: 0 0 1rem; max-width: 68ch; }
article ul, article ol { margin: 0 0 1rem; padding-left: 1.4rem; max-width: 68ch; }
article li { margin-bottom: 0.5rem; }
article hr { border: none; border-top: 1px solid var(--rule); margin: 2rem 0; }
article code {
  font-family: ui-monospace, "SF Mono", Menlo, Consolas, monospace;
  background: var(--code-bg); padding: 0.05rem 0.35rem; border-radius: 3px; font-size: 0.88em;
}
article pre {
  background: var(--code-bg); padding: 0.9rem 1rem; border-radius: 6px;
  overflow-x: auto; margin: 0 0 1.2rem;
}
article pre code { background: none; padding: 0; font-size: 0.85rem; line-height: 1.5; }
article pre code.hljs { background: none; padding: 0; }
article pre.mermaid {
  background: var(--surface); border: 1px solid var(--rule);
  display: flex; justify-content: center; overflow-x: auto;
}
article table {
  border-collapse: collapse; width: 100%; margin: 0 0 1.4rem; font-size: 0.92rem;
  display: block; overflow-x: auto;
}
article th, article td {
  border: 1px solid var(--rule); padding: 0.45rem 0.7rem; text-align: left; vertical-align: top;
}
article th { background: var(--code-bg); font-weight: 600; }
article strong { font-weight: 700; }
article a { color: var(--accent); }
article del {
  color: var(--del-text); background: var(--del-bg); text-decoration: line-through;
  text-decoration-thickness: 1.5px;
}
article ins {
  color: var(--ins-text); background: var(--ins-bg); text-decoration: underline;
  text-decoration-thickness: 1.5px;
}
article .diff-block {
  margin: 0 -1rem 1rem; padding: 0.15rem 1rem; border-radius: 6px;
}
article .diff-block.removed { background: var(--del-bg); }
article .diff-block.added { background: var(--ins-bg); }
article .diff-block > :last-child { margin-bottom: 0.6rem; }
article li.diff-li { margin: 0 -0.6rem; padding: 0.1rem 0.6rem; border-radius: 4px; list-style-position: inside; }
article li.diff-li.removed { background: var(--del-bg); }
article li.diff-li.added { background: var(--ins-bg); }
body:has(#changebar) { padding-right: 22px; }
#changebar {
  position: fixed; top: 0; right: 0; width: 22px; height: 100vh;
  background: var(--surface); border-left: 1px solid var(--rule); z-index: 100;
}
#changebar .changebar-label {
  position: absolute; top: 0.5rem; left: 0; right: 0; text-align: center;
  font-family: ui-monospace, "SF Mono", Menlo, Consolas, monospace;
  font-size: 0.62rem; color: var(--ink-muted); writing-mode: vertical-rl;
  letter-spacing: 0.05em; pointer-events: none;
}
.changebar-tick {
  position: absolute; left: 4px; right: 4px; height: 10px; min-height: 10px;
  border-radius: 2px; cursor: pointer; transform: translateY(-50%);
  box-shadow: 0 0 0 1px rgba(0, 0, 0, 0.15);
}
.changebar-tick.removed { background: var(--del-text); }
.changebar-tick.added { background: var(--ins-text); }
.changebar-tick:hover { outline: 2px solid var(--ink); outline-offset: 1px; }
"""

CHANGEBAR_SCRIPT = """
(function () {
  var regions = [];
  document.querySelectorAll('.diff-block, li.diff-li').forEach(function (el) {
    regions.push({ el: el, cls: el.classList.contains('removed') ? 'removed' : 'added' });
  });
  document.querySelectorAll('article del, article ins').forEach(function (el) {
    if (el.closest('.diff-block, li.diff-li')) return;
    regions.push({ el: el, cls: el.tagName.toLowerCase() === 'del' ? 'removed' : 'added' });
  });
  if (regions.length === 0) return;

  var bar = document.createElement('div');
  bar.id = 'changebar';
  var label = document.createElement('div');
  label.className = 'changebar-label';
  label.textContent = regions.length + ' change' + (regions.length === 1 ? '' : 's');
  bar.appendChild(label);
  document.body.appendChild(bar);

  function layout() {
    var docHeight = document.documentElement.scrollHeight;
    bar.querySelectorAll('.changebar-tick').forEach(function (t) { t.remove(); });
    regions.forEach(function (r) {
      var top = r.el.getBoundingClientRect().top + window.scrollY;
      var pct = docHeight > 0 ? (top / docHeight) * 100 : 0;
      var tick = document.createElement('div');
      tick.className = 'changebar-tick ' + r.cls;
      tick.style.top = pct + '%';
      tick.title = (r.cls === 'removed' ? 'removed' : 'added') + ' — click to jump here';
      tick.addEventListener('click', function () {
        r.el.scrollIntoView({ behavior: 'smooth', block: 'center' });
      });
      bar.appendChild(tick);
    });
  }
  layout();
  window.addEventListener('resize', layout);
})();
"""

# Loaded only on pages that actually contain a `pre.mermaid` block (see
# render_page) — CDN-hosted since this is a local-only dev tool, not
# distributed content. Theme follows the OS setting the same way PAGE_CSS's
# custom properties do, so diagrams don't clash with light/dark mode.
MERMAID_SCRIPT = """
<script type="module">
  import mermaid from 'https://cdn.jsdelivr.net/npm/mermaid@11/dist/mermaid.esm.min.mjs';
  mermaid.initialize({
    startOnLoad: true,
    theme: window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'default',
  });
</script>
"""

# Loaded only on pages with a fenced code block (see render_page). Same
# CDN-hosted rationale as MERMAID_SCRIPT. github/github-dark cover both
# themes; "jsonc" is the only fence language used in these docs and isn't
# one of highlight.js's built-in names, so it's aliased to "json" — the
# `//`-comment/trailing-comma allowances jsonc implies aren't things
# highlight.js's json grammar enforces against anyway, so plain json
# tokenization reads it fine.
HIGHLIGHT_SCRIPT = """
<link id="hljs-theme" rel="stylesheet">
<script src="https://cdn.jsdelivr.net/npm/highlight.js@11/lib/highlight.min.js"></script>
<script>
  document.getElementById('hljs-theme').href = window.matchMedia('(prefers-color-scheme: dark)').matches
    ? 'https://cdn.jsdelivr.net/npm/highlight.js@11/styles/github-dark.min.css'
    : 'https://cdn.jsdelivr.net/npm/highlight.js@11/styles/github.min.css';
  hljs.registerAliases('jsonc', { languageName: 'json' });
  hljs.highlightAll();
</script>
"""


def render_page(body_html: str, title: str, summary: str, meta_extra: str, show_changebar: bool = False) -> str:
    generated_at = datetime.now(timezone.utc).strftime("%Y-%m-%d %H:%M UTC")
    script_tag = f"<script>{CHANGEBAR_SCRIPT}</script>" if show_changebar else ""
    mermaid_tag = MERMAID_SCRIPT if 'class="mermaid"' in body_html else ""
    highlight_tag = HIGHLIGHT_SCRIPT if '<code class="language-' in body_html else ""
    return f"""<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{html.escape(title)}</title>
<style>{PAGE_CSS}</style>
</head>
<body>
<div class="page">
  <a class="back" href="index.html">&larr; all docs</a>
  <p class="meta">{html.escape(summary)} &middot; {meta_extra} &middot; generated {generated_at}</p>
  <article>
{body_html}
  </article>
</div>
{script_tag}
{mermaid_tag}
{highlight_tag}
</body>
</html>
"""


def render_index(manifest: list[dict]) -> str:
    items = []
    for entry in sorted(manifest, key=lambda e: e["title"].lower()):
        if entry["kind"] == "diff":
            stats = (
                f'<span class="stats"><span class="added">+{entry["added"]}</span> '
                f'<span class="removed">-{entry["removed"]}</span></span>'
            )
        else:
            stats = f'<span class="stats">{entry["lines"]} lines</span>'
        items.append(
            '<li><a href="{slug}.html">{title}</a> <span class="kind">{kind}</span>{stats}'
            '<p class="summary">{summary}</p>'
            '<p class="ts">generated {ts}</p></li>'.format(
                slug=html.escape(entry["slug"]),
                title=html.escape(entry["title"]),
                kind=html.escape(entry["kind"]),
                stats=stats,
                summary=html.escape(entry["summary"]),
                ts=html.escape(entry["generated_at"]),
            )
        )
    return f"""<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Doc diffs</title>
<style>
:root {{
  --bg: #f7f4ee; --surface: #ffffff; --ink: #211d16; --ink-muted: #6b6355;
  --rule: #ddd4c0; --ins-text: #1f6b2e; --del-text: #8a2318;
}}
@media (prefers-color-scheme: dark) {{
  :root {{
    --bg: #16130e; --surface: #1e1a13; --ink: #ece5d6; --ink-muted: #a89d87;
    --rule: #332c20; --ins-text: #7fd98f; --del-text: #ff8f7d;
  }}
}}
* {{ box-sizing: border-box; }}
body {{ margin: 0; background: var(--bg); color: var(--ink);
  font-family: -apple-system, "Segoe UI", Helvetica, Arial, sans-serif; }}
main {{ max-width: 640px; margin: 0 auto; padding: 3rem 1.5rem 4rem; }}
h1 {{ font-size: 1.6rem; margin: 0 0 0.4rem; }}
p.lede {{ color: var(--ink-muted); margin: 0 0 2rem; }}
ul {{ list-style: none; margin: 0; padding: 0; }}
li {{ border-bottom: 1px solid var(--rule); padding: 1.1rem 0; }}
li a {{ font-size: 1.05rem; font-weight: 600; color: var(--ink); text-decoration: none; }}
li a:hover {{ text-decoration: underline; }}
.kind {{
  margin-left: 0.6rem; font-family: ui-monospace, Menlo, Consolas, monospace; font-size: 0.72rem;
  text-transform: uppercase; letter-spacing: 0.04em; color: var(--ink-muted);
  border: 1px solid var(--rule); border-radius: 3px; padding: 0.1rem 0.4rem;
}}
.stats {{ margin-left: 0.6rem; font-family: ui-monospace, Menlo, Consolas, monospace; font-size: 0.85rem; }}
.stats .added {{ color: var(--ins-text); font-weight: 700; }}
.stats .removed {{ color: var(--del-text); font-weight: 700; }}
p.summary {{ margin: 0.35rem 0 0; color: var(--ink-muted); }}
p.ts {{ margin: 0.25rem 0 0; font-size: 0.78rem; color: var(--ink-muted); opacity: 0.75; }}
</style>
</head>
<body>
<main>
  <h1>Doc diffs &amp; readers</h1>
  <p class="lede">Local view of doc content, always rendered as a preview and always in full — nothing collapsed, truncated, or left as raw source. Not committed — regenerate with <code>ops/scripts/gen_nip_diff.py</code>.</p>
  <ul>
  {''.join(items)}
  </ul>
</main>
</body>
</html>
"""


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--old", type=pathlib.Path, help="prior version; omit for read mode (rendered preview, no diff)")
    ap.add_argument("--new", required=True, type=pathlib.Path, help="current version of the file")
    ap.add_argument("--title", required=True)
    ap.add_argument("--slug", required=True)
    ap.add_argument("--summary", default="")
    ap.add_argument("--out-dir", default="docs/artifacts", type=pathlib.Path)
    args = ap.parse_args()

    new_text = args.new.read_text()

    if args.old is not None:
        old_text = args.old.read_text()
        body_html, added, removed = render_document_diff(old_text, new_text)
        kind = "diff"
        meta_extra = f'<span class="added">+{added}</span> <span class="removed">-{removed}</span> words'
        manifest_entry = {"kind": kind, "added": added, "removed": removed}
    else:
        body_html = render_document(new_text)
        line_count = len(new_text.splitlines())
        kind = "read"
        meta_extra = f"{line_count} lines"
        manifest_entry = {"kind": kind, "lines": line_count}

    page_html = render_page(body_html, args.title, args.summary, meta_extra, show_changebar=(kind == "diff"))

    args.out_dir.mkdir(parents=True, exist_ok=True)
    (args.out_dir / f"{args.slug}.html").write_text(page_html)

    manifest_path = args.out_dir / "manifest.json"
    manifest = json.loads(manifest_path.read_text()) if manifest_path.exists() else []
    manifest = [e for e in manifest if e["slug"] != args.slug]
    manifest.append({
        "slug": args.slug,
        "title": args.title,
        "summary": args.summary,
        "generated_at": datetime.now(timezone.utc).strftime("%Y-%m-%d %H:%M UTC"),
        **manifest_entry,
    })
    manifest_path.write_text(json.dumps(manifest, indent=2))
    (args.out_dir / "index.html").write_text(render_index(manifest))

    print(f"wrote {args.out_dir / (args.slug + '.html')} ({kind})")
    print(f"wrote {args.out_dir / 'index.html'}")


if __name__ == "__main__":
    main()
