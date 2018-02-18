OUT := pdns-etcd3
GIT_VERSION := $(shell git describe --always --dirty)

RM ?= rm -f
GOPATH := $(realpath $(dir $(lastword $(MAKEFILE_LIST))))/lib

.PHONY: all fmt vet clean

all: $(OUT) vet

$(OUT): $(wildcard src/*.go)
	@$(MAKE) --no-print-directory fmt
	CGO_ENABLED=0 go build -o $(OUT) -a -ldflags="-extldflags=-static -X main.gitVersion=${GIT_VERSION}" ./src

fmt:
	gofmt -l -s -w src

vet:
	-go vet ./src

clean:
	$(RM) $(OUT)
