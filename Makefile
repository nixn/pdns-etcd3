OUT := pdns-etcd3
GIT_VERSION := $(shell git describe --always --dirty)

RM ?= rm -f

GOLINT := $(shell command -v golint 2>/dev/null)
STATICCHECK := $(shell command -v staticcheck 2>/dev/null)

VERBOSE ?= 0
ifeq ($(VERBOSE),1)
TEST_EXTRA_ARGS += -v
endif

ifneq ($(ONLY),)
TEST_EXTRA_ARGS += -run $(ONLY)
endif

.PHONY: all fmt vet lint stan clean unit-tests unit-tests+coverage integration-tests integration-tests+coverage tests tests+coverage

all: $(OUT) vet lint stan unit-tests integration-tests

$(OUT): pdns-etcd3.go $(wildcard src/*.go)
	@$(MAKE) --no-print-directory fmt
	CGO_ENABLED=0 go build -o $(OUT) -a -ldflags="-extldflags=-static -X main.gitVersion=${GIT_VERSION}"

fmt:
	gofmt -l -s -w pdns-etcd3.go src

vet:
	-go vet

lint:
ifeq ($(GOLINT),)
	@echo "(golint not found)"
else
	-golint ./...
endif

stan:
ifeq ($(STATICCHECK),)
	@echo "(staticcheck not found)"
else
	-staticcheck -tags unit,integration ./...
endif

clean:
	$(RM) $(OUT) coverage.*

unit-tests:
	go test -tags unit $(TEST_EXTRA_ARGS) ./src

unit-tests+coverage:
	-go test -tags unit -coverprofile=coverage.unit.txt $(TEST_EXTRA_ARGS) ./src
	go tool cover -html=coverage.unit.txt -o coverage.unit.html

integration-tests:
	go test -tags integration -count=1 $(TEST_EXTRA_ARGS) ./src

integration-tests+coverage:
	-go test -tags integration -count=1 -coverprofile=coverage.integration.txt $(TEST_EXTRA_ARGS) ./src
	go tool cover -html=coverage.integration.txt -o coverage.integration.html

tests: unit-tests integration-tests
	@echo "tests finished"

tests+coverage:
	-go test -tags unit,integration -count=1 -coverprofile=coverage.txt $(TEST_EXTRA_ARGS) ./src
	go tool cover -html=coverage.txt -o coverage.html
