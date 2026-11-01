# 0001: One standard-library Go file

**Decision:** the tool is a single Go file using `go/parser`, with no type checking and no dependencies.

**Why:** people reviewing Go services already have Go, so `go run` is the whole install, with no npm package or binaries. Parsing needs no build, so it works on code that doesn't compile here, and never runs anyone's code.

**Alternatives:** `golang.org/x/tools/go/analysis` gives type information and fewer false findings, but needs a dependency and a buildable project. Regular expressions in Python would need no Go, but would be much less accurate.
