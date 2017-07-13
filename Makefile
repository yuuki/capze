NAME=capze
COMMIT = $$(git describe --always)
PKGS = $$(go list ./... | grep -v vendor)

.PHONY: all
all: build

.PHONY: build
build:
	go build -ldflags "-X main.GitCommit=\"$(COMMIT)\"" -o bin/$(NAME)

.PHONY: test
test:
	go test -v $(PKGS)

.PHONY: vet
vet:
	go tool vet -all -printfuncs=Wrap,Wrapf,Errorf $$(find . -maxdepth 1 -mindepth 1 -type d | grep -v -e "^\.\/\." -e vendor)

.PHONY: patch
patch: gobump
	./script/release.sh patch

.PHONY: minor
minor: gobump
	./script/release.sh minor

.PHONY: gobump
gobump:
	go get github.com/motemen/gobump/cmd/gobump

