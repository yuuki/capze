NAME=capze
COMMIT = $$(git describe --always)

all: build

deps:
	go get -d -t -v .

build: deps
	go build -ldflags "-X main.GitCommit=\"$(COMMIT)\"" -o bin/$(NAME)

test:
	go test -v .

vet:
	go vet ./...


.PHONY: all deps build test
