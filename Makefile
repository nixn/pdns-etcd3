OUT := pdns-etcd3
VERSION ?= $(shell git describe --always --dirty)

RM ?= rm -f
GOPATH := $(realpath $(dir $(lastword $(MAKEFILE_LIST))))/lib

.PHONY: fmt vet clean

$(OUT): $(wildcard src/*.go)
	@$(MAKE) --no-print-directory fmt
	CGO_ENABLED=0 go build -o $(OUT) -a -ldflags="-extldflags=-static -X main.version=${VERSION}" ./src
	@$(MAKE) --no-print-directory vet

fmt:
	gofmt -l -s -w src

vet:
	go vet ./src

clean:
	$(RM) $(OUT)
