.PHONY: build install clean

BINARY := silo
ENTITLEMENTS := silo.entitlements
GOBIN ?= $(shell go env GOBIN)
ifeq ($(GOBIN),)
GOBIN := $(shell go env GOPATH)/bin
endif

build:
	go build -o $(BINARY) .
	codesign --sign - --entitlements $(ENTITLEMENTS) --force $(BINARY)

install: build
	mkdir -p $(GOBIN)
	cp $(BINARY) $(GOBIN)/$(BINARY)

clean:
	rm -f $(BINARY)
