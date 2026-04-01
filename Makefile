IMG ?= ghcr.io/jvillalbaj2lc/k8-hot-shrunk-requests:latest

.PHONY: fmt vet lint test build run docker-build docker-push

fmt:
	go fmt ./...

vet:
	go vet ./...

lint:
	golangci-lint run ./...

test:
	go test ./... -v -count=1

build:
	CGO_ENABLED=0 go build -o bin/manager .

run:
	go run . --health-probe-bind-address=:8081

docker-build:
	docker build -t $(IMG) .

docker-push:
	docker push $(IMG)
