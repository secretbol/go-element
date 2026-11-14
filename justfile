default:
    @just --list

# pure-Go Olm (goolm) so no libolm install is needed; sqlite still needs cgo
build:
    go build -tags goolm -o go-element ./cmd/go-element

test:
    go test -tags goolm -race ./...

lint:
    golangci-lint run -c .golangci.yml

tidy:
    go mod tidy
