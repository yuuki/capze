NAME=caplize

all: build

deps:
	go get -d -t -v .

build: deps
	go build -o bin/${NAME}

.PHONY: all deps build
