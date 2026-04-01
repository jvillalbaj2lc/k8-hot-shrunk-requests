IMG ?= cpu-shrink-controller:latest

.PHONY: fmt vet test build run docker-build

fmt:
	go fmt ./...

vet:
	go vet ./...

test:
	go test ./... -v -count=1

build:
	CGO_ENABLED=0 go build -o bin/manager .

run:
	go run . --health-probe-bind-address=:8081

docker-build:
	docker build -t $(IMG) .
