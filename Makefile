.PHONY: build test vet fmt gate install clean

PREFIX ?= $(HOME)/.local
BINDIR ?= $(PREFIX)/bin

build:
	go build -o picu .

test:
	go test -count=1 ./...

vet:
	go vet ./...

fmt:
	gofmt -l .

gate: fmt vet test build

install: build
	mkdir -p $(BINDIR)
	cp picu $(BINDIR)/picu

clean:
	rm -f picu
