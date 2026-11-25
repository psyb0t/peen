#!/usr/bin/env python3
"""Prepare docs/ for the Docusaurus site in website/.

    python3 -I scripts/make/docs_prepare.py [SITE_DIRECTORY]

docs/ stays plain Markdown that reads correctly on GitHub. This script copies it
into SITE_DIRECTORY/.content (the Docusaurus docs directory) and
SITE_DIRECTORY/.assets (static files). SITE_DIRECTORY defaults to website/;
scripts/make/docs.sh passes its scratch copy of it.

  - each page keeps the URL its file name gives it, or its `permalink`;
  - `parent` and `nav_order` become .content/sidebar.json;
  - llms.txt is docs/llms.txt followed by one line per page;
  - api/api.yml becomes .assets/reference/api.yml, which the site offers for
    download and docusaurus-plugin-openapi-docs turns into the REST reference.

It refuses Liquid or Jekyll syntax, which Docusaurus would print literally, and
the front matter mistakes docs-and-prose.md forbids. A refusal exits nonzero
with one message naming the file.
"""

import json
import re
import shutil
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
DOCS = ROOT / "docs"
SITE = Path(sys.argv[1]).resolve() if len(sys.argv) > 1 else ROOT / "website"
CONTENT = SITE / ".content"
ASSETS = SITE / ".assets"
API_SPEC = ROOT / "api" / "api.yml"
API_SPEC_ASSET = ASSETS / "reference" / "api.yml"
SITE_ORIGIN = "https://psyb0t.github.io"
BASE_URL = "/peen"
INDEX_PAGE = "index.md"
FRONT_MATTER = re.compile(r"\A---\n(.*?)\n---\n", re.S)
# A Liquid tag, a filter such as relative_url, or a site/page variable.
# GitHub Actions expressions such as ${{ secrets.X }} pass.
LIQUID = re.compile(r"\{%|\{\{[^}]*\|\s*\w+|\{\{\s*(site|page|item)\.")
TOP_HEADING = re.compile(r"^# ", re.M)
FENCE = re.compile(r"^(```|~~~).*?^\1", re.S | re.M)
DESCRIPTION_MIN = 50
DESCRIPTION_MAX = 170
REQUIRED_KEYS = ("title", "description", "nav_order")
KEPT_KEYS = ("title", "description")
NAVIGATION_KEYS = {"parent", "nav_order", "nav_exclude", "search_exclude", "permalink"}


def fail(path, message):
    raise SystemExit(f"{path.relative_to(ROOT)}: {message}")


def parse_front_matter(path):
    text = path.read_text(encoding="utf-8")
    match = FRONT_MATTER.match(text)
    if not match:
        fail(path, "missing front matter")
    fields = {}
    for line in match.group(1).splitlines():
        if not line.strip() or line.startswith((" ", "\t")):
            continue
        key, _, value = line.partition(":")
        value = value.strip()
        if len(value) >= 2 and value[0] == value[-1] and value[0] in "\"'":
            value = value[1:-1]
        fields[key.strip()] = value
    unknown = set(fields) - NAVIGATION_KEYS - set(KEPT_KEYS)
    if unknown:
        fail(path, f"unsupported front matter {sorted(unknown)}")
    return fields, text[match.end():]


def check_page(path, fields, body):
    for key in REQUIRED_KEYS:
        if not fields.get(key):
            fail(path, f"missing {key}")
    length = len(fields["description"])
    if not DESCRIPTION_MIN <= length <= DESCRIPTION_MAX:
        fail(path, f"description is {length} characters, not {DESCRIPTION_MIN} to {DESCRIPTION_MAX}")
    liquid = LIQUID.search(body)
    if liquid:
        line = body[:liquid.start()].count("\n") + 1
        fail(path, f"Liquid syntax near body line {line}; write plain Markdown")
    headings = len(TOP_HEADING.findall(FENCE.sub("", body)))
    if headings != 1:
        fail(path, f"has {headings} top-level headings, not one")


def slug_of(path, fields):
    if path.name == INDEX_PAGE:
        return "/"
    permalink = fields.get("permalink") or f"/{path.stem}/"
    return permalink.rstrip("/")


def page_url(slug):
    return BASE_URL + ("/" if slug == "/" else slug + "/")


def check_unique(pages, key):
    seen = {}
    for name, (fields, _) in pages.items():
        value = fields[key]
        if value in seen:
            raise SystemExit(f"docs/{name} and docs/{seen[value]} share the {key} {value!r}")
        seen[value] = name


def write_page(name, fields, body, slug):
    header = [f"{key}: {json.dumps(fields[key], ensure_ascii=False)}" for key in KEPT_KEYS]
    header.append(f"slug: {json.dumps(slug)}")
    (CONTENT / name).write_text("---\n" + "\n".join(header) + "\n---\n" + body, encoding="utf-8")


def build_sidebar(pages):
    by_title = {fields["title"]: name for name, (fields, _) in pages.items()}
    children = {}
    roots = []
    for name, (fields, _) in pages.items():
        if fields.get("nav_exclude") == "true":
            continue
        parent = fields.get("parent")
        if parent and parent not in by_title:
            raise SystemExit(f"docs/{name}: parent {parent!r} is not a page title")
        (children.setdefault(by_title[parent], []) if parent else roots).append(name)

    def order(name):
        fields = pages[name][0]
        return (float(fields["nav_order"]), fields["title"].lower())

    def item(name):
        doc_id = name.removesuffix(".md")
        kids = sorted(children.get(name, []), key=order)
        if not kids:
            return doc_id
        return {
            "type": "category",
            "label": pages[name][0]["title"],
            "link": {"type": "doc", "id": doc_id},
            "items": [item(kid) for kid in kids],
        }

    return [item(name) for name in sorted(roots, key=order)]


def build_llms(pages, slugs):
    header = (DOCS / "llms.txt").read_text(encoding="utf-8").rstrip() + "\n\n"
    rows = sorted(
        (fields["title"], slugs[name], fields["description"])
        for name, (fields, _) in pages.items()
        if fields.get("search_exclude") != "true"
    )
    lines = [f"- [{title}]({SITE_ORIGIN}{page_url(slug)}): {description}" for title, slug, description in rows]
    (ASSETS / "llms.txt").write_text(header + "\n".join(lines) + "\n", encoding="utf-8")


def main():
    for directory in (CONTENT, ASSETS):
        if directory.exists():
            shutil.rmtree(directory)
        directory.mkdir(parents=True)
    pages = {path.name: parse_front_matter(path) for path in sorted(DOCS.glob("*.md"))}
    for name, (fields, body) in pages.items():
        check_page(DOCS / name, fields, body)
    check_unique(pages, "title")
    check_unique(pages, "description")
    slugs = {name: slug_of(DOCS / name, fields) for name, (fields, _) in pages.items()}
    for name, (fields, body) in pages.items():
        write_page(name, fields, body, slugs[name])
    sidebar = build_sidebar(pages)
    (CONTENT / "sidebar.json").write_text(json.dumps(sidebar, indent=2) + "\n", encoding="utf-8")
    build_llms(pages, slugs)
    API_SPEC_ASSET.parent.mkdir(parents=True)
    shutil.copyfile(API_SPEC, API_SPEC_ASSET)
    print(f"prepared {len(pages)} pages and the REST reference spec")


if __name__ == "__main__":
    sys.exit(main())
