#!/usr/bin/env python3
"""Check the documentation site links that Docusaurus cannot check.

    python3 -I scripts/make/docs_check_site_links.py BUILD_DIRECTORY

docs/ links the generated REST reference pages, and README.md and the published
skill link the site, with full https://psyb0t.github.io/peen/... URLs, so they
also work on GitHub and Docker Hub. Docusaurus treats those as external and
never checks them. This script resolves each one against the built site: the
page or file must exist, and a #fragment must match an id on that page. A miss
exits nonzero with one line per broken link.
"""

import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
SITE_URL = "https://psyb0t.github.io/peen/"
SITE_LINK = re.compile(re.escape(SITE_URL) + r"([^)\s\"'<>`]*)")
SOURCES = ("docs/*.md", "README.md", ".agents/skills/**/*.md")
INDEX_PAGE = "index.html"


def built_target(build, path):
    target = build / path
    if not path or path.endswith("/"):
        return target / INDEX_PAGE
    return target


def has_anchor(page, fragment):
    html = page.read_text(encoding="utf-8")
    return f'id="{fragment}"' in html


def broken_links(build):
    for pattern in SOURCES:
        for source in sorted(ROOT.glob(pattern)):
            text = source.read_text(encoding="utf-8")
            for match in SITE_LINK.finditer(text):
                path, _, fragment = match.group(1).partition("#")
                target = built_target(build, path)
                line = text[:match.start()].count("\n") + 1
                where = f"{source.relative_to(ROOT)}:{line}"
                if not target.is_file():
                    yield f"{where}: {SITE_URL}{match.group(1)} names no built page"
                    continue
                if fragment and not has_anchor(target, fragment):
                    yield f"{where}: {SITE_URL}{match.group(1)} names no anchor on its page"


def main():
    if len(sys.argv) != 2:
        raise SystemExit("usage: docs_check_site_links.py BUILD_DIRECTORY")
    build = Path(sys.argv[1]).resolve()
    broken = list(broken_links(build))
    if broken:
        raise SystemExit("\n".join(broken))
    print("every site link resolves")


if __name__ == "__main__":
    sys.exit(main())
