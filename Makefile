.PHONY: build test fmt vet docker-build run

build:
	mkdir -p bin
	go build -o bin/brsaver ./cmd/brsaver

test:
	go test ./...

fmt:
	gofmt -w $$(find cmd internal -name '*.go' -type f)

vet:
	go vet ./...

docker-build:
	docker build -t brsaver:local .

run:
	go run ./cmd/brsaver
