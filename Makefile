.PHONY: check generate

check:
	mise exec -- go test ./...
	mise exec -- go vet ./...
	mise exec -- go run ./cmd/modelcatalog -check

generate:
	mise exec -- go run ./cmd/modelcatalog
