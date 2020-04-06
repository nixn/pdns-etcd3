OUT := pdns-etcd3
GIT_VERSION := $(shell git describe --always --dirty)

RM ?= rm -f

.PHONY: all fmt vet clean

all: $(OUT) vet

$(OUT): pdns-etcd3.go $(wildcard src/*.go)
	@$(MAKE) --no-print-directory fmt
	CGO_ENABLED=0 go build -o $(OUT) -a -ldflags="-extldflags=-static -X main.gitVersion=${GIT_VERSION}"

fmt:
	gofmt -l -s -w pdns-etcd3.go src

vet:
	-go vet

clean:
	$(RM) $(OUT)
