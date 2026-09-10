#!/usr/bin/env python3
"""Generate a local HTML page for reading or diffing a doc (e.g. docs/nips/*.md).

Two modes, both rendering the file's complete content — nothing is ever
collapsed, truncated, or hidden behind a click:

  diff mode (--old and --new both given): a track-changes-style view —
  removed text struck through in red, added text underlined in green,
  word-level within changed lines.

  read mode (--old omitted): the full current content of --new, plain,
  numbered, no diff markup — for just reading a NIP end to end.

Writes one HTML page per run into an output directory and rebuilds that
directory's index.html from its manifest.json.

This is dev tooling for reviewing/reading prose/spec docs before they land —
not part of the build. Output is regenerated on demand and is not meant to
be committed; see docs/artifacts/ in .gitignore.

Usage:
    # diff mode
    python3 ops/scripts/gen_nip_diff.py \
        --old /path/to/before.md --new docs/nips/NIP-CASH.md \
        --title "NIP-CASH.md" --slug nip-cash \
        --summary "Trimmed three repeated rationale passages." \
        --out-dir docs/artifacts

    # read mode — full current content, no diff
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


def build_diff_rows(old_text: str, new_text: str):
    """Line-level diff; changed-line pairs get a nested word-level diff.

    Returns (rows, added, removed) where each row is
    (old_lineno|None, new_lineno|None, css_class, html_content). Every line
    of both files is included — nothing is ever collapsed or omitted.
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


def build_read_rows(text: str):
    """Every line of `text`, plain — no diff, no collapsing."""
    return [(i + 1, i + 1, "ctx", html.escape(line)) for i, line in enumerate(text.splitlines())]


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
"""


def render_page(rows, title: str, summary: str, stats_html: str) -> str:
    body = []
    for old_no, new_no, css, content in rows:
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
<title>{html.escape(title)}</title>
<style>{PAGE_CSS}</style>
</head>
<body>
<header>
  <a class="back" href="index.html">&larr; all docs</a>
  <h1>{html.escape(title)}</h1>
  <p class="summary">{html.escape(summary)}</p>
  <p class="stats">{stats_html} &middot; generated {generated_at}</p>
</header>
<table class="diff"><tbody>
{"".join(body)}
</tbody></table>
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
  <p class="lede">Local view of doc content — full text always, nothing collapsed or truncated. Not committed — regenerate with <code>ops/scripts/gen_nip_diff.py</code>.</p>
  <ul>
  {''.join(items)}
  </ul>
</main>
</body>
</html>
"""


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--old", type=pathlib.Path, help="prior version; omit for read mode (full current content, no diff)")
    ap.add_argument("--new", required=True, type=pathlib.Path, help="current version of the file")
    ap.add_argument("--title", required=True)
    ap.add_argument("--slug", required=True)
    ap.add_argument("--summary", default="")
    ap.add_argument("--out-dir", default="docs/artifacts", type=pathlib.Path)
    args = ap.parse_args()

    new_text = args.new.read_text()

    if args.old is not None:
        old_text = args.old.read_text()
        rows, added, removed = build_diff_rows(old_text, new_text)
        kind = "diff"
        stats_html = f'<span class="added">+{added}</span> <span class="removed">-{removed}</span>'
        manifest_entry = {"kind": kind, "added": added, "removed": removed}
    else:
        rows = build_read_rows(new_text)
        kind = "read"
        line_count = len(new_text.splitlines())
        stats_html = f"{line_count} lines"
        manifest_entry = {"kind": kind, "lines": line_count}

    args.out_dir.mkdir(parents=True, exist_ok=True)
    (args.out_dir / f"{args.slug}.html").write_text(
        render_page(rows, args.title, args.summary, stats_html)
    )

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
