all: build

deps:
	go get -d -t -v .

build: deps
	go build -o bin/capdir

.PHONY: all deps build
