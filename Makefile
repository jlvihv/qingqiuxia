.PHONY: build release test race bench e2e lint fmt run headless snapshot clean

# build keeps the symbol table and the debug info: a crash prints a stack with
# file and line numbers, and a debugger can walk it. It is what to work with.
build:
	go build -o bin/qingqiuxia ./cmd/qingqiuxia

# release is the same binary without them, and it is a third smaller: 23.5 MB
# stripped to 16.6 MB. Nothing a user of the tool sees changes — the symbol
# table and the DWARF information are only ever read by a debugger, a profiler
# or a stack trace, and "go tool pprof" against a matching source tree still
# resolves every function.
#
# The version is stamped in at the same time. The variable lives in main and
# defaults to "0.1.0-dev"; a release should say what it is.
# The version a release stamps in. Override it on the command line:
#
#	make release VERSION=1.2.0
#
# It defaults to the source's own, so that a release built without saying a
# version does not end up with an empty one — which -X would happily write.
VERSION ?= 0.1.0-dev

release:
	go build -trimpath -ldflags="-s -w -X main.version=$(VERSION)" -o bin/qingqiuxia ./cmd/qingqiuxia

run: build
	./bin/qingqiuxia

# the core alone: for a machine with no display, or a CI job
headless: build
	./bin/qingqiuxia -headless

# render the window to a PNG, for a machine with no display
snapshot:
	QQX_RENDER=$(CURDIR)/bin/window.png go test ./internal/ui -run TestRenderLayout

test:
	go test ./...

race:
	go test ./... -race

bench:
	go test ./internal/proxy -bench . -benchtime 500x -run XXX

e2e:
	./scripts/e2e.sh

fmt:
	gofmt -w .

vet:
	go vet ./...

clean:
	rm -rf bin
