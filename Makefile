OS_GO_BIN_NAME=go
ifeq ($(shell uname),Windows)
	OS_GO_BIN_NAME=go.exe
endif

OS_GO_OS=$(shell $(OS_GO_BIN_NAME) env GOOS)
# toggle to fake being windows..
#OS_GO_OS=windows

BIN_ROOT=$(PWD)/.bin
BIN=$(BIN_ROOT)/storrent
ifeq ($(OS_GO_OS),windows)
	BIN=$(BIN_ROOT)/storrent.exe
endif

DATA_ROOT=$(PWD)/.data
DATA=$(DATA_ROOT)/storrent.json

.PHONY: help
help: # Show help for each of the Makefile recipes.
	@grep -E '^[a-zA-Z0-9 -]+:.*#'  Makefile | sort | while read -r l; do printf "\033[1;32m$$(echo $$l | cut -f 1 -d':')\033[00m:$$(echo $$l | cut -f 2- -d'#')\n"; done

print: # print for local system
	@echo ""
	@echo "OS_GO_BIN_NAME:  $(OS_GO_BIN_NAME)"
	@echo ""
	@echo "OS_GO_OS:  $(OS_GO_OS)"
	@echo ""
	@echo "BIN_ROOT:  $(BIN_ROOT)"
	@echo "BIN:       $(BIN)"
	@echo ""
	@echo "DATA_ROOT: $(DATA_ROOT)"
	@echo "DATA:      $(DATA)"
	@echo ""

ci-build: # build for ci, that can be called from Windows, MacOS or Linux
	# You can call this locally. github workflow also calls it.
	# Its calling everything in the makefile ...
	@echo ""
	@echo "CI BUILD starting ..."
	$(MAKE) help 
	$(MAKE) print 
	$(MAKE) clean-all
	$(MAKE) build-debug
	$(MAKE) data-bootstrap
	$(MAKE) run-debug
	@echo ""
	@echo "CI BUILD ended ...."

clean-all: data-clean build-clean # cleans all data and binaries

upgrade:
	# https://github.com/oligot/go-mod-upgrade
	# https://github.com/oligot/go-mod-upgrade/releases/tag/v0.9.1
	go install github.com/oligot/go-mod-upgrade@v0.9.1
	go-mod-upgrade
	go mod tidy

build-init: # init binaries
	mkdir -p $(BIN_ROOT)
build-clean: # clean binaries
	rm -rf $(BIN_ROOT)
build: build-init # build release binaries
	CGO_ENABLED=1 go build -o $(BIN) .
build-debug: build-init # build debug binaries
	CGO_ENABLED=1 go build -ldflags='-s -w' -o $(BIN) .


data-init: # init data folder
	mkdir -p $(DATA_ROOT)
data-clean: # clean data folder
	rm -rf $(DATA_ROOT)

# toggle to choose what example you want to use...
#DATA_EXAMPLE=examples/01/galene-ldap.json
äDATA_EXAMPLE=examples/02/galene-ldap.json
data-bootstrap: data-init # create data
	cp $(DATA_EXAMPLE) $(DATA)

	# TODO: cp in certs...Gen with mkcert.

run-h: # run to show help
	$(BIN) -h
run: # run release
	$(BIN) -data $(DATA_ROOT)
	# http://localhost:8088
run-debug: # run debug
	nohup $(BIN) -debug -data $(DATA_ROOT) &
	# http://localhost:8088

