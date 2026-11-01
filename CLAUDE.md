# gosturdy

Claude Code skill and Go CLI that finds production risks in Go services.

## Architecture
`plugin/scripts/gosturdy.go` holds all the logic in one standard-library file, so `go run` needs no setup. `Scan` parses files, `scanFile` checks each one, and `checkAcrossFiles` applies rules that need the whole service. `plugin/skills/reviewing-go-services/SKILL.md` tells Claude how to use it. See `docs/architecture.md`.

## Commands
- `make test`: gofmt, vet and tests
- `make lint`: manifest validation
- `make demo`: scan `examples/badservice`

## Conventions
- Standard library only. Never build or run the scanned code.
- Each rule has a stable `GSnnn` ID, a fix, and tests for what it flags and what it must not. Don't reuse IDs.
- A file that won't parse must be reported, never skipped silently.

## Do and don't
- Do run a new rule on a real project and read what it flags before adding it.
- Don't say a service is production ready.

## Definition of done
`make test` and `make lint` pass, and `CHANGELOG.md`, `ROADMAP.md` and docs are updated when relevant.
