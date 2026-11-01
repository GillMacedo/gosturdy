# Architecture

    Scan --> parse each non-test file --> readIgnores --> scanFile
    scanFile --> checkLiteral, checkCall, checkHandler --> findings and facts
    checkAcrossFiles --> rules that need the whole service, like a server with no shutdown

- **Imports are resolved by name,** so `web.Get` counts when `web` is `net/http`, and another package's `http.Get` doesn't.
- **Facts cover the whole scan,** because shutdown and pool settings often live in another file.
- **The skill** has Claude confirm each finding, because the parser has no types.

Key choice: one standard-library file run with `go run`. See `decisions/`.
