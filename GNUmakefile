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
	go test -v -cover -timeout=120s -parallel=10 ./...

verify:
	bash scripts/verify.sh
	$(MAKE) test-e2e
	golangci-lint run

test-e2e:
	python3 scripts/check-e2e.py

testacc:
	TF_ACC=1 go test -v -cover -timeout 120m ./...

.PHONY: fmt lint test test-e2e testacc verify build install generate
