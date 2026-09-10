.PHONY: test test-connect build vet

SHEN ?= shen

test:
	go test ./...

test-connect:
	HUGINN_SHEN="$(SHEN)" go test ./cmd/huginn -run 'TestConnection' -count=1

vet:
	go vet ./...

build:
	mkdir -p bin plugins/huginn-channel/bin
	go build -o bin/huginn ./cmd/huginn
	go build -o bin/huginn-mcp ./cmd/huginn-mcp
	go build -o bin/huginn-channel ./cmd/huginn-channel
	cp bin/huginn-channel plugins/huginn-channel/bin/huginn-channel
