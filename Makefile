OUT := pdns-etcd3
GIT_VERSION := $(shell git describe --always --dirty)

RM ?= rm -f

.PHONY: all fmt vet lint clean test

all: $(OUT) vet lint

$(OUT): pdns-etcd3.go $(wildcard src/*.go)
	@$(MAKE) --no-print-directory fmt
	CGO_ENABLED=0 go build -o $(OUT) -a -ldflags="-extldflags=-static -X main.gitVersion=${GIT_VERSION}"

fmt:
	gofmt -l -s -w pdns-etcd3.go src

vet:
	-go vet

lint:
	-golint ./...

clean:
	$(RM) $(OUT)

test:
	go test ./src
