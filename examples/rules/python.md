---
paths:
  - "**/*.py"
  - "**/*.pyi"
---

# Python

Sources: [PEP 8](https://peps.python.org/pep-0008/), [PEP 257](https://peps.python.org/pep-0257/), and [PEP 484](https://peps.python.org/pep-0484/).

Follow the project's own formatter, linter, and type checker settings, usually in `pyproject.toml`, and the style of the surrounding code first. Use this rule where the project says nothing.

- Name functions and variables in `snake_case`, classes in `CapWords`, and constants in `UPPER_CASE`.
- Add type hints to public functions, and run the project's type checker, such as mypy or pyright, when it has one.
- Give public modules, classes, and functions docstrings.
- Catch specific exceptions. Never write a bare `except:`. When you raise a new exception from a caught one, use `raise NewError(...) from err`.
- Open files and locks with `with`.
- Never use a mutable default argument such as `def f(items=[])`. Use `None` and create the value inside.
- Use `logging` instead of `print` outside of command-line entry points.
- Prefer `pathlib.Path` for paths and f-strings for formatting.
- Run the project's tests, usually `pytest`, before you report the work as done.
- Add a dependency only through the project's dependency file and tool, such as `pyproject.toml` with uv, Poetry, or pip.
