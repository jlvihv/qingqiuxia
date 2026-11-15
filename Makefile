.PHONY: build test race bench e2e lint fmt run headless snapshot clean

build:
	go build -o bin/qingqiuxia ./cmd/qingqiuxia

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
