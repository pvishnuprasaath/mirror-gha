.PHONY: build test vet fmt fmt-check install run-example clean

BINARY := bin/mirror

build:
	go build -o $(BINARY) ./cmd/mirror

test:
	go test ./... -v

vet:
	go vet ./...

fmt:
	gofmt -w $(shell find . -name '*.go' -not -path './third_party/*')

fmt-check:
	@unformatted="$$(gofmt -l . | grep -v '^third_party/' || true)"; \
	if [ -n "$$unformatted" ]; then \
		echo "not gofmt'd:"; echo "$$unformatted"; exit 1; \
	fi

install: build
	cp $(BINARY) $(GOPATH)/bin/mirror

run-example: build
	$(BINARY) run examples/workflows/basic-run.yml

clean:
	rm -rf bin/
