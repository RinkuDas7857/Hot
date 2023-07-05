.PHONY: validate verify verify_ruby verify_golang test test_ruby test_golang coverage coverage_golang setup _make_necessary_dirs build compile check clean install

FIPS_MODE ?= 0
OS := $(shell uname)
GO_SOURCES := $(shell git ls-files \*.go)
VERSION_STRING := $(shell git describe --match v* 2>/dev/null || awk '$$0="v"$$0' VERSION 2>/dev/null || echo unknown)
BUILD_TIME := $(shell date -u +%Y%m%d.%H%M%S)
BUILD_TAGS := tracer_static tracer_static_jaeger continuous_profiler_stackdriver

export GOFLAGS := -mod=readonly

ifeq (${FIPS_MODE}, 1)
    # Go 1.19 now requires GOEXPERIMENT=boringcrypto for FIPS compilation.
    # See https://github.com/golang/go/issues/51940 for more details.
    BORINGCRYPTO_SUPPORT := $(shell GOEXPERIMENT=boringcrypto go version > /dev/null 2>&1; echo $$?)
    ifeq ($(BORINGCRYPTO_SUPPORT), 0)
        GOBUILD_ENV=GOEXPERIMENT=boringcrypto
    endif

    BUILD_TAGS += fips
    # If the golang-fips compiler is built with CGO_ENABLED=0, this needs to be
    # explicitly switched on.
    export CGO_ENABLED=1
endif

ifeq (${OS}, Darwin) # Mac OS
    BREW_PREFIX := $(shell brew --prefix 2>/dev/null || echo "/opt/homebrew")

    # To be able to compile gssapi library
    export CGO_CFLAGS="-I$(BREW_PREFIX)/opt/heimdal/include"
endif

GOBUILD_FLAGS := -ldflags "-X main.Version=$(VERSION_STRING) -X main.BuildTime=$(BUILD_TIME)" -tags "$(BUILD_TAGS)" -mod=mod

PREFIX ?= /usr/local

build: compile

validate: verify test

verify: verify_golang

verify_golang:
	gofmt -s -l $(GO_SOURCES) | awk '{ print } END { if (NR > 0) { print "Please run make fmt"; exit 1 } }'

fmt:
	gofmt -w -s $(GO_SOURCES)

test: test_ruby test_golang

# The Ruby tests are now all integration specs that test the Go implementation.
test_ruby:
	bundle exec rspec --color --format d spec

test_golang:
	go test -cover -coverprofile=cover.out -count 1 ./...

test_golang_race:
	go test -race -count 1 ./...

coverage: coverage_golang

coverage_golang:
	[ -f cover.out ] && go tool cover -func cover.out

setup: _make_necessary_dirs bin/gitlab-shell

_make_necessary_dirs:
	support/make_necessary_dirs

compile: bin/gitlab-shell bin/gitlab-sshd
bin/gitlab-shell: $(GO_SOURCES)
	GOBIN="$(CURDIR)/bin" $(GOBUILD_ENV) go install $(GOBUILD_FLAGS) ./cmd/...

bin/gitlab-sshd: $(GO_SOURCES)
	GOBIN="$(CURDIR)/bin" $(GOBUILD_ENV) go install $(GOBUILD_FLAGS) ./cmd/gitlab-sshd

check:
	bin/check

clean:
	rm -f bin/check bin/gitlab-shell bin/gitlab-shell-authorized-keys-check bin/gitlab-shell-authorized-principals-check bin/gitlab-sshd

install: compile
	mkdir -p $(DESTDIR)$(PREFIX)/bin/
	install -m755 bin/check $(DESTDIR)$(PREFIX)/bin/check
	install -m755 bin/gitlab-shell $(DESTDIR)$(PREFIX)/bin/gitlab-shell
	install -m755 bin/gitlab-shell-authorized-keys-check $(DESTDIR)$(PREFIX)/bin/gitlab-shell-authorized-keys-check
	install -m755 bin/gitlab-shell-authorized-principals-check $(DESTDIR)$(PREFIX)/bin/gitlab-shell-authorized-principals-check
	install -m755 bin/gitlab-sshd $(DESTDIR)$(PREFIX)/bin/gitlab-sshd
