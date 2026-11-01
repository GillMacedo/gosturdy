# gosturdy: production readiness check for Go services

[![CI](https://github.com/GillMacedo/gosturdy/actions/workflows/ci.yml/badge.svg)](https://github.com/GillMacedo/gosturdy/actions/workflows/ci.yml) [![MIT license](https://img.shields.io/badge/license-MIT-blue)](LICENSE)

**A Claude Code skill and Go CLI that finds production risks in a Go service: HTTP servers and clients with no timeouts, no graceful shutdown, unlimited database pools, ignored request contexts and unbounded request bodies.**

These problems pass tests and show up later, as requests dropped during a deploy or a pool of goroutines stuck on a dead server. `gosturdy` checks the whole service at once, so it can see that a server starts in one file and nothing anywhere shuts it down. The skill then has Claude read each finding in context and confirm or dismiss it.

## What you can ask Claude

- "Is this Go service ready for production?"
- "We drop requests every time we deploy. Why?"
- "Review this Go API for missing timeouts."
- "Why does this service run out of database connections?"

## Install

As a Claude Code plugin:

    claude plugin marketplace add GillMacedo/gosturdy
    claude plugin install gosturdy@gosturdy-marketplace

Or run it directly with Go 1.22 or newer. It has no dependencies:

    go run github.com/GillMacedo/gosturdy/plugin/scripts@latest ./my-service

## Example

On the made-up service in `examples/badservice`:

    $ make demo
    examples/badservice: 9 findings in 1 Go files (tests and generated code skipped)

    HIGH   GS201 main.go:21
      http.Get uses the default client, which has no timeout
      Fix: Use an http.Client with a Timeout. The default client waits forever on a stuck server.

    HIGH   GS101 main.go:41
      http.ListenAndServe has no timeouts and can't shut down gracefully
      Fix: Use an http.Server with timeouts, and call Shutdown when the process gets SIGTERM.

    HIGH   GS301 main.go:41
      a server starts here, and nothing in this scan calls Shutdown or GracefulStop
      Fix: Stop the server on SIGTERM and call Shutdown, so a deploy doesn't cut off requests in flight.

    HIGH   GS302 main.go:41
      a server starts here, and nothing in this scan listens for SIGTERM
      Fix: Use signal.NotifyContext for SIGTERM and SIGINT, then start the shutdown from it.

    MEDIUM GS503 main.go:14
      reads the whole request body, and nothing in this scan limits body size
      Fix: Wrap the body with http.MaxBytesReader, so one large request can't exhaust memory.

    MEDIUM GS502 main.go:18
      this goroutine uses r after the handler may have returned
      Fix: Copy what the goroutine needs first. The request and its context end when the handler returns.

    MEDIUM GS501 main.go:27
      context.Background inside a request handler ignores the request's cancellation
      Fix: Use the request's context, so work stops when the client goes away or a deadline passes.

    MEDIUM GS401 main.go:35
      sql.Open here, and nothing in this scan calls SetMaxOpenConns
      Fix: Call SetMaxOpenConns, so a traffic spike can't exhaust the database's connections.

    LOW    GS402 main.go:35
      sql.Open here, and nothing in this scan sets a connection lifetime
      Fix: Call SetConnMaxLifetime, so connections get replaced after failovers and network changes.

    These come from the code's shape, without running it. Check each one, because a wrapper or framework may already handle it.

Add `--format markdown` or `--format json`. `--fail-on` sets the severity that exits 1 in CI: `high` by default, or `medium`, `low` or `none`. Exit code 2 means bad options. A folder with no Go files, or a file that won't parse, also exits 1, so a scan that checked nothing can't pass.

To accept a risk, add a comment on the line or the line before it: `//gosturdy:ignore GS201 health check with its own deadline`.

## Rules

| Rule | Severity | Finds |
|---|---|---|
| GS101 | High | `http.ListenAndServe` and similar, with no timeouts or shutdown. Medium next to a graceful server, where it's often a debug port |
| GS102 | High | `http.Server` with no `ReadHeaderTimeout` or `ReadTimeout` |
| GS103 | Medium | `http.Server` missing other timeouts |
| GS201 | High | `http.Get`, `http.Post`, `http.DefaultClient`: no timeout |
| GS202 | Medium | `http.Client` with no `Timeout` |
| GS301 | High | A server starts, and nothing calls `Shutdown` or `GracefulStop` |
| GS302 | High | A server starts, and nothing listens for signals |
| GS401 | Medium | `sql.Open` with no `SetMaxOpenConns` |
| GS402 | Low | `sql.Open` with no connection lifetime |
| GS501 | Medium | `context.Background` or `TODO` inside a request handler |
| GS502 | Medium | A goroutine in a handler that uses the request |
| GS503 | Medium | Reading a whole request body with no size limit |
| GS601 | Medium | `log.Fatal` or `os.Exit` outside `package main` |

It recognizes server starts for `net/http`, Gin, Echo, Fiber and gRPC, and handlers for `net/http`, Gin, Echo and Fiber.

## How it compares

[golangci-lint](https://github.com/golangci/golangci-lint) is great and you should keep using it. Some of its linters overlap with single rules here, such as `gosec` on server timeouts. `gosturdy` is narrower: it looks across a whole service for production risks, and the skill turns the findings into a reviewed list.

## Limits

- **It reads shape, not types.** It never builds or runs your code, so a wrapper, middleware or framework can make a finding wrong, and it can miss what's hidden behind one. Check each finding.
- **A clean result doesn't mean ready.** It doesn't cover runtime behavior, Kubernetes settings, retries, observability or load.
- **Facts cover the whole scanned folder.** In a monorepo, one service's `Shutdown` can hide another's missing one, so scan each service's folder.
- **Other pools and clients aren't covered yet,** such as `pgxpool` and `sqlx`. Frameworks with their own lifecycle, like Kratos, aren't flagged and aren't checked.
- Tests, generated files, `vendor`, `testdata` and hidden folders are skipped.

## FAQ

**How do I check if a Go service is production ready?** Start with this for the code-level risks, then review what it doesn't cover, listed above.

**Why does my Go service drop requests during deploys?** Often it stops at SIGTERM without draining. `GS301` and `GS302` look for that, though your Kubernetes grace period matters too.

**Why does an HTTP call in Go hang forever?** `http.Get` and a default `http.Client` have no timeout. `GS201` and `GS202` find them.

**Is it an MCP server?** No. It's a skill with one Go source file, so `go run` is all it needs.

## Tested on

Unit tests cover what each rule flags and what it must not. It also scanned `go-clean-arch`, `go-clean-template`, `kratos-layout` and `ardanlabs/service`. I read the source for those findings, which led to two changes: a logger's own `Fatal` method no longer counts, and a bare listener next to a graceful server is no longer high severity. It hasn't been tested in a fresh Claude session.

## Contributing

Contributions are welcome, from people and from people working with AI agents. See [CONTRIBUTING.md](CONTRIBUTING.md). Good first issues are [listed here](https://github.com/GillMacedo/gosturdy/issues?q=is%3Aissue+is%3Aopen+label%3A%22good+first+issue%22).

## AI disclosure

This project was largely generated with Claude Code and reviewed by its maintainer.

## About me

I'm Giulliano Macedo, and I build developer tools at GoTech Solutions. Follow along or get in touch:

- GitHub: [GillMacedo](https://github.com/GillMacedo)
- LinkedIn: [giulliano](https://www.linkedin.com/in/giulliano/)
- X: [@giumacedo](https://x.com/giumacedo)

If `gosturdy` caught something before production did, a star helps others find it.

---

Built by [Giulliano Macedo](https://github.com/GillMacedo), GoTech Solutions · [LinkedIn](https://www.linkedin.com/in/giulliano/) · [X](https://x.com/giumacedo)
