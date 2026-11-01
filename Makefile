.PHONY: test lint demo

test:
	test -z "$$(gofmt -l plugin examples)"
	go vet ./...
	go test -race ./...

lint:
	claude plugin validate ./plugin --strict
	claude plugin validate . --strict

# Exit 1 means it found high severity problems, which the example has on purpose.
demo:
	go run plugin/scripts/gosturdy.go examples/badservice || test $$? -eq 1
