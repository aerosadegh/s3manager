.PHONY: build
build:
	go build -o bin/s3manager

.PHONY: run
run:
	go run .

.PHONY: lint
lint:
	golangci-lint run

.PHONY: test
test:
	go test -race -cover ./...

.PHONY: build-image
build-image:
	podman build -t s3manager .

# MinIO with an admin app and a read-only viewer, see compose.test.yaml.
COMPOSE ?= podman compose
.PHONY: test-stack
test-stack:
	CGO_ENABLED=0 GOOS=linux go build -o test/stack/bin/s3manager .
	CGO_ENABLED=0 GOOS=linux go build -o test/stack/bin/seed ./test/seed
	$(COMPOSE) -f compose.test.yaml up

.PHONY: clean
clean:
	rm -rf bin
