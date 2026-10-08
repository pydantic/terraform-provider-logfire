default: fmt lint install generate

build:
	go build -v ./...

install: build
	go install -v ./...

lint:
	golangci-lint run

generate:
	cd tools; go generate ./...

fmt:
	gofmt -s -w -e .

test:
	go test -v -cover -timeout=5m -parallel=10 ./...

test-e2e:
	TF_ACC= go test -v -count=1 -timeout=5m -run '^TestLifecycle' ./internal/provider/

testacc:
	TF_ACC=1 go test -v -cover -timeout 120m ./...

.PHONY: fmt lint test test-e2e testacc build install generate
