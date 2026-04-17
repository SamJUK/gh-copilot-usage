BINARY := gh-copilot-usage
VERSION := 0.1.0

.PHONY: build install clean test vet coverage

build:
	go build -ldflags="-s -w" -o $(BINARY) .

install: build
	gh extension install .

test:
	go test -race ./...

vet:
	go vet ./...

coverage:
	go test -race -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out

clean:
	rm -f $(BINARY) coverage.out
