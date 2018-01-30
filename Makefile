SOURCES := $(wildcard *.go)
OUT := pdns-etcd3
VERSION ?= $(shell git describe --dirty)

.PHONY: all
all: fmt $(OUT) vet

$(OUT): $(SOURCES)
	CGO_ENABLED=0 go build -i -o $(OUT) -ldflags="-extldflags=-static -X main.version=${VERSION}"

.PHONY: fmt
fmt:
	gofmt -s -w $(SOURCES)

.PHONY: vet
vet:
	go vet

.PHONY: clean
clean:
	$(RM) $(OUT)
