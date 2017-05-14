NAME=capze
COMMIT = $$(git describe --always)

all: build

build:
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

.PHONY: all deps build test vet patch minor gobump
