OUT := pdns-etcd3
VERSION := $(shell git describe --always --long --dirty)

.PHONY: all
all: fmt build vet

.PHONY: build
build:
	go build -i -v -o ${OUT} -ldflags="-X main.version=${VERSION}"

.PHONY: fmt
fmt:
	gofmt -l -s -w .

.PHONY: vet
vet:
	go vet

.PHONY: clean
clean:
	$(RM) ${OUT}
