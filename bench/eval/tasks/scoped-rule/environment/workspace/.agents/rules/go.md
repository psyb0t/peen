---
paths:
  - "**/*.go"
---

# Go conventions in this project

- Every exported function has a doc comment that starts with the function's name.
- Every error that package calc returns is created with `errors.New` and its message starts with `calc: `, such as `errors.New("calc: negative input")`.
- Package calc never panics.
