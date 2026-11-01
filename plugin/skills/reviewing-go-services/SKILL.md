---
name: reviewing-go-services
description: Reviews a Go service for production risks by parsing its source, including HTTP servers and clients with no timeouts, no graceful shutdown on SIGTERM, database connection pools with no limits, request contexts ignored in handlers, goroutines that outlive requests, unbounded request bodies and libraries that exit the process. Use when the user asks if a Go service is production ready, wants a Go backend reviewed before launch, or is debugging hangs, dropped requests during deploys or connection exhaustion.
---

# Reviewing Go services

Run the bundled tool first. It only parses source, so it needs Go installed but never builds or runs the project.

    go run "${CLAUDE_PLUGIN_ROOT}/scripts/gosturdy.go" PATH

Add `--format markdown` for a table. Exit code 1 means high severity findings, or files it couldn't parse.

## Check each finding before reporting it

The tool sees code shape, without types. So for each finding, read the code:

- A wrapper, middleware or framework may already handle it.
- `GS101` next to a graceful server may be a debug or metrics port. Check that it isn't public.
- `GS201` and `GS202` matter less if every request has a context deadline. Confirm that rather than assuming.
- Say which findings you confirmed, and which you dismissed and why.

## Report

1. List confirmed risks, worst first, with `file:line` and what happens in production.
2. Show the smallest fix for each one, matching the project's style.
3. Say what this doesn't cover: runtime behavior, Kubernetes settings such as probes and `terminationGracePeriodSeconds`, retries, observability and load. Don't call a service production ready because the tool found nothing.

## Don't

Don't change code unless the user asks. Timeouts need values that fit the service, so propose them and explain the choice. Add `//gosturdy:ignore RULE reason` only when the user agrees the risk is accepted.
