OUT := pdns-etcd3
GIT_VERSION := $(shell git describe --always --dirty)

RM ?= rm -f

GOCILINT := $(shell command -v golangci-lint 2>/dev/null)

VERBOSE ?= 0
ifeq ($(VERBOSE),1)
TEST_EXTRA_ARGS += -v
endif

ifneq ($(ONLY),)
TEST_EXTRA_ARGS += -run $(ONLY)
endif

.PHONY: all fmt vet gocilint clean unit-tests unit-tests+coverage integration-tests integration-tests+coverage tests tests+coverage

all: $(OUT) vet gocilint unit-tests integration-tests

$(OUT): pdns-etcd3.go $(wildcard src/*.go)
	@$(MAKE) --no-print-directory fmt
	CGO_ENABLED=0 go build -o $(OUT) -a -ldflags="-extldflags=-static -X main.gitVersion=${GIT_VERSION}"

fmt:
	gofmt -l -s -w pdns-etcd3.go src

vet:
	-go vet

gocilint:
ifeq ($(GOCILINT),)
	@echo "(golangci-lint not found)"
else
	-$(GOCILINT) run
endif

clean:
	$(RM) $(OUT) coverage.*

unit-tests:
	go test -tags unit $(TEST_EXTRA_ARGS) ./src

unit-tests+coverage:
	-go test -tags unit -coverprofile=coverage.unit.txt $(TEST_EXTRA_ARGS) ./src
	go tool cover -html=coverage.unit.txt -o coverage.unit.html

integration-tests:
	@export ETCD_VERSION PDNS_VERSION
	go test -tags integration -count=1 $(TEST_EXTRA_ARGS) ./src

integration-tests+coverage:
	@export ETCD_VERSION PDNS_VERSION
	-go test -tags integration -count=1 -coverprofile=coverage.integration.txt $(TEST_EXTRA_ARGS) ./src
	go tool cover -html=coverage.integration.txt -o coverage.integration.html

tests: unit-tests integration-tests

tests+coverage:
	@export ETCD_VERSION PDNS_VERSION
	-go test -tags unit,integration -count=1 -coverprofile=coverage.txt $(TEST_EXTRA_ARGS) ./src
	go tool cover -html=coverage.txt -o coverage.html

.PHONY: integration-tests-matrix integration-tests-matrix-etcd integration-tests-matrix-pdns

ETCD_VERSIONS := 3.2.32 3.3.27 3.4.40 3.5.26 3.6.7
PDNS_VERSIONS := 34 40 41 44 45 46 47 48 49 50 51

integration-tests-matrix-etcd:
	-@set -u; \
	for etcd_version in $(ETCD_VERSIONS); do \
	  echo ETCD_VERSION=$$etcd_version ; \
	  $(MAKE) --no-print-directory integration-tests ETCD_VERSION=$$etcd_version ONLY=PipeRequests ; \
	done

integration-tests-matrix-pdns:
	-@set -u; \
	for pdns_version in $(PDNS_VERSIONS); do \
	  echo PDNS_VERSION=$$pdns_version ; \
	  $(MAKE) --no-print-directory integration-tests PDNS_VERSION=$$pdns_version ONLY=PDNS ; \
	done

integration-tests-matrix: integration-tests-matrix-etcd integration-tests-matrix-pdns
