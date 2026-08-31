.PHONY: test build vet

test:
	go test ./...

vet:
	go vet ./...

build:
	mkdir -p bin
	go build -o bin/huginn ./cmd/huginn
