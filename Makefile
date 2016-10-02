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

patch: gobump
	./script/release.sh patch

minor: gobump
	./script/release.sh minor

gobump:
	go get github.com/motemen/gobump/cmd/gobump

.PHONY: all deps build test
