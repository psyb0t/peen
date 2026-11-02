# ParseSize

`func ParseSize(s string) (int64, error)` turns a human-written size into a number of bytes.

- The input is a whole number followed by an optional unit. One space may separate them. Surrounding whitespace is ignored.
- No unit means bytes. `B` also means bytes.
- `KB`, `MB`, and `GB` are powers of 1000: `2KB` is 2000.
- `KiB`, `MiB`, and `GiB` are powers of 1024: `2KiB` is 2048.
- Units are case-insensitive: `5mb` and `5MB` are the same.
- Return a non-nil error for an empty string, a negative number, a number with a fraction such as `1.5MB`, an unknown unit, or a result that does not fit in an `int64`.

| Input | Result |
| --- | --- |
| `"0"` | `0` |
| `"512"` | `512` |
| `"10 KB"` | `10000` |
| `"3MiB"` | `3145728` |
| `"1gb"` | `1000000000` |
