#!/usr/bin/env python3
"""Generate a local, track-changes-style HTML diff between two text files.

Renders removed text struck through in red and added text underlined in green,
inline in place (word-level within changed lines, full lines for pure
adds/removes), with long unchanged runs collapsed behind a click-to-expand
marker. Writes one HTML page per run into an output directory and rebuilds
that directory's index.html from its manifest.json.

This is dev tooling for reviewing prose/spec edits (e.g. docs/nips/*.md)
before they land — not part of the build. Output is regenerated on demand and
is not meant to be committed; see docs/artifacts/ in .gitignore.

Usage:
    python3 ops/scripts/gen_nip_diff.py \
        --old /path/to/before.md --new docs/nips/NIP-CASH.md \
        --title "NIP-CASH.md" --slug nip-cash \
        --summary "Trimmed three repeated rationale passages." \
        --out-dir docs/artifacts
"""
import argparse
import difflib
import html
import json
import pathlib
import re
from datetime import datetime, timezone

CONTEXT_LINES = 3
COLLAPSE_THRESHOLD = CONTEXT_LINES * 2 + 2

WORD_RE = re.compile(r"\s+|\S+")


def word_tokenize(line: str) -> list[str]:
    return WORD_RE.findall(line)


def render_word_diff(old_line: str, new_line: str) -> str:
    old_tokens = word_tokenize(old_line)
    new_tokens = word_tokenize(new_line)
    sm = difflib.SequenceMatcher(None, old_tokens, new_tokens, autojunk=False)
    out = []
    for tag, i1, i2, j1, j2 in sm.get_opcodes():
        if tag == "equal":
            out.append(html.escape("".join(old_tokens[i1:i2])))
        elif tag == "delete":
            out.append(f'<del>{html.escape("".join(old_tokens[i1:i2]))}</del>')
        elif tag == "insert":
            out.append(f'<ins>{html.escape("".join(new_tokens[j1:j2]))}</ins>')
        elif tag == "replace":
            out.append(f'<del>{html.escape("".join(old_tokens[i1:i2]))}</del>')
            out.append(f'<ins>{html.escape("".join(new_tokens[j1:j2]))}</ins>')
    return "".join(out)


def build_rows(old_text: str, new_text: str):
    """Line-level diff; changed-line pairs get a nested word-level diff.

    Returns (rows, added, removed) where each row is
    (old_lineno|None, new_lineno|None, css_class, html_content).
    """
    old_lines = old_text.splitlines()
    new_lines = new_text.splitlines()
    sm = difflib.SequenceMatcher(None, old_lines, new_lines, autojunk=False)
    rows = []
    oln = nln = 0
    added = removed = 0

    for tag, i1, i2, j1, j2 in sm.get_opcodes():
        if tag == "equal":
            for k in range(i1, i2):
                oln += 1
                nln += 1
                rows.append((oln, nln, "ctx", html.escape(old_lines[k])))
        elif tag == "delete":
            for k in range(i1, i2):
                oln += 1
                removed += 1
                rows.append((oln, None, "del-line", f"<del>{html.escape(old_lines[k])}</del>"))
        elif tag == "insert":
            for k in range(j1, j2):
                nln += 1
                added += 1
                rows.append((None, nln, "ins-line", f"<ins>{html.escape(new_lines[k])}</ins>"))
        elif tag == "replace":
            old_block = old_lines[i1:i2]
            new_block = new_lines[j1:j2]
            pair_n = min(len(old_block), len(new_block))
            for k in range(pair_n):
                oln += 1
                nln += 1
                added += 1
                removed += 1
                rows.append((oln, nln, "chg-line", render_word_diff(old_block[k], new_block[k])))
            for k in range(pair_n, len(old_block)):
                oln += 1
                removed += 1
                rows.append((oln, None, "del-line", f"<del>{html.escape(old_block[k])}</del>"))
            for k in range(pair_n, len(new_block)):
                nln += 1
                added += 1
                rows.append((None, nln, "ins-line", f"<ins>{html.escape(new_block[k])}</ins>"))

    return rows, added, removed


def collapse_context(rows):
    """Replace long unchanged runs with a click-to-expand marker that carries
    the hidden rows along with it, keeping CONTEXT_LINES around each edit."""
    out = []
    group_id = 0
    i, n = 0, len(rows)
    while i < n:
        if rows[i][2] != "ctx":
            out.append(rows[i])
            i += 1
            continue
        j = i
        while j < n and rows[j][2] == "ctx":
            j += 1
        run_len = j - i
        if run_len <= COLLAPSE_THRESHOLD:
            out.extend(rows[i:j])
        else:
            out.extend(rows[i:i + CONTEXT_LINES])
            hidden = rows[i + CONTEXT_LINES:j - CONTEXT_LINES]
            group_id += 1
            out.append(("collapse", len(hidden), hidden, group_id))
            out.extend(rows[j - CONTEXT_LINES:j])
        i = j
    return out


PAGE_CSS = """
:root {
  --bg: #f7f4ee; --surface: #ffffff; --ink: #211d16; --ink-muted: #6b6355;
  --rule: #ddd4c0; --del-bg: #fbe4e1; --del-text: #8a2318; --ins-bg: #e1f2e2; --ins-text: #1f6b2e;
  --gutter: #a89c86;
}
@media (prefers-color-scheme: dark) {
  :root {
    --bg: #16130e; --surface: #1e1a13; --ink: #ece5d6; --ink-muted: #a89d87;
    --rule: #332c20; --del-bg: #3a1c17; --del-text: #ff8f7d; --ins-bg: #15321a; --ins-text: #7fd98f;
    --gutter: #6b6353;
  }
}
* { box-sizing: border-box; }
body {
  margin: 0; background: var(--bg); color: var(--ink);
  font-family: ui-monospace, "SF Mono", "Cascadia Code", Menlo, Consolas, monospace;
  font-size: 13.5px;
}
header { max-width: 900px; margin: 0 auto; padding: 2.5rem 1.5rem 1.5rem; }
header .back { color: var(--ink-muted); text-decoration: none; font-size: 0.85rem; }
header .back:hover { color: var(--ink); }
h1 {
  font-family: -apple-system, "Segoe UI", Helvetica, Arial, sans-serif;
  font-size: 1.4rem; margin: 0.6rem 0 0.3rem;
}
.summary {
  font-family: -apple-system, "Segoe UI", Helvetica, Arial, sans-serif;
  color: var(--ink-muted); margin: 0 0 0.6rem; max-width: 60ch;
}
.stats { margin: 0; }
.stats .added { color: var(--ins-text); font-weight: 700; }
.stats .removed { color: var(--del-text); font-weight: 700; }
table.diff { width: 100%; border-collapse: collapse; }
table.diff td { padding: 0 0.6rem; vertical-align: top; white-space: pre-wrap; word-break: break-word; }
td.ln {
  color: var(--gutter); text-align: right; user-select: none; width: 3.2rem;
  white-space: nowrap; padding-right: 0.8rem;
}
tr.ctx td.content { color: var(--ink); }
tr.del-line { background: var(--del-bg); }
tr.ins-line { background: var(--ins-bg); }
del {
  color: var(--del-text); background: var(--del-bg); text-decoration: line-through;
  text-decoration-thickness: 1.5px;
}
ins {
  color: var(--ins-text); background: var(--ins-bg); text-decoration: underline;
  text-decoration-thickness: 1.5px;
}
tr.collapse-marker td.content {
  color: var(--ink-muted); font-style: italic; cursor: pointer; padding: 0.35rem 0.6rem;
  border-top: 1px dashed var(--rule); border-bottom: 1px dashed var(--rule);
}
tr.collapse-marker:hover td.content { color: var(--ink); }
tr.hidden-block { display: none; }
tr.hidden-block.shown { display: table-row; }
"""


def render_page(rows, title: str, summary: str, added: int, removed: int) -> str:
    body = []
    for row in rows:
        if row[0] == "collapse":
            _, count, hidden_rows, group_id = row
            body.append(
                f'<tr class="collapse-marker" data-target="g{group_id}">'
                f'<td class="ln" colspan="2"></td>'
                f'<td class="content">&ctdot; {count} unchanged lines — click to expand &ctdot;</td></tr>'
            )
            for old_no, new_no, _css, content in hidden_rows:
                body.append(
                    f'<tr class="hidden-block" data-hidden-for="g{group_id}">'
                    f'<td class="ln">{old_no or ""}</td><td class="ln">{new_no or ""}</td>'
                    f'<td class="content">{content}</td></tr>'
                )
            continue
        old_no, new_no, css, content = row
        body.append(
            f'<tr class="{css}"><td class="ln">{old_no or ""}</td><td class="ln">{new_no or ""}</td>'
            f'<td class="content">{content}</td></tr>'
        )

    generated_at = datetime.now(timezone.utc).strftime("%Y-%m-%d %H:%M UTC")

    return f"""<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{html.escape(title)} — diff</title>
<style>{PAGE_CSS}</style>
</head>
<body>
<header>
  <a class="back" href="index.html">&larr; all diffs</a>
  <h1>{html.escape(title)}</h1>
  <p class="summary">{html.escape(summary)}</p>
  <p class="stats"><span class="added">+{added}</span> <span class="removed">-{removed}</span> &middot; generated {generated_at}</p>
</header>
<table class="diff"><tbody>
{"".join(body)}
</tbody></table>
<script>
document.querySelectorAll('.collapse-marker').forEach(function(row) {{
  row.addEventListener('click', function() {{
    document.querySelectorAll('[data-hidden-for="' + row.dataset.target + '"]').forEach(function(r) {{
      r.classList.toggle('shown');
    }});
    row.classList.toggle('open');
  }});
}});
</script>
</body>
</html>
"""


def render_index(manifest: list[dict]) -> str:
    items = []
    for entry in sorted(manifest, key=lambda e: e["title"].lower()):
        items.append(
            '<li><a href="{slug}.html">{title}</a>'
            '<span class="stats"><span class="added">+{added}</span> '
            '<span class="removed">-{removed}</span></span>'
            '<p class="summary">{summary}</p>'
            '<p class="ts">generated {ts}</p></li>'.format(
                slug=html.escape(entry["slug"]),
                title=html.escape(entry["title"]),
                added=entry["added"],
                removed=entry["removed"],
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
.stats {{ margin-left: 0.8rem; font-family: ui-monospace, Menlo, Consolas, monospace; font-size: 0.85rem; }}
.stats .added {{ color: var(--ins-text); font-weight: 700; }}
.stats .removed {{ color: var(--del-text); font-weight: 700; }}
p.summary {{ margin: 0.35rem 0 0; color: var(--ink-muted); }}
p.ts {{ margin: 0.25rem 0 0; font-size: 0.78rem; color: var(--ink-muted); opacity: 0.75; }}
</style>
</head>
<body>
<main>
  <h1>Doc diffs</h1>
  <p class="lede">Local track-changes view of pending doc edits. Not committed — regenerate with <code>ops/scripts/gen_nip_diff.py</code>.</p>
  <ul>
  {''.join(items)}
  </ul>
</main>
</body>
</html>
"""


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--old", required=True, type=pathlib.Path)
    ap.add_argument("--new", required=True, type=pathlib.Path)
    ap.add_argument("--title", required=True)
    ap.add_argument("--slug", required=True)
    ap.add_argument("--summary", default="")
    ap.add_argument("--out-dir", default="docs/artifacts", type=pathlib.Path)
    args = ap.parse_args()

    old_text = args.old.read_text()
    new_text = args.new.read_text()

    rows, added, removed = build_rows(old_text, new_text)
    rows = collapse_context(rows)

    args.out_dir.mkdir(parents=True, exist_ok=True)
    (args.out_dir / f"{args.slug}.html").write_text(
        render_page(rows, args.title, args.summary, added, removed)
    )

    manifest_path = args.out_dir / "manifest.json"
    manifest = json.loads(manifest_path.read_text()) if manifest_path.exists() else []
    manifest = [e for e in manifest if e["slug"] != args.slug]
    manifest.append({
        "slug": args.slug,
        "title": args.title,
        "summary": args.summary,
        "added": added,
        "removed": removed,
        "generated_at": datetime.now(timezone.utc).strftime("%Y-%m-%d %H:%M UTC"),
    })
    manifest_path.write_text(json.dumps(manifest, indent=2))
    (args.out_dir / "index.html").write_text(render_index(manifest))

    print(f"wrote {args.out_dir / (args.slug + '.html')}  (+{added} -{removed})")
    print(f"wrote {args.out_dir / 'index.html'}")


if __name__ == "__main__":
    main()
