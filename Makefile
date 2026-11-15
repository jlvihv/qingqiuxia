.PHONY: build test race bench e2e lint fmt run gui snapshot clean

build:
	go build -o bin/qingqiuxia ./cmd/qingqiuxia
	go build -o bin/qingqiuxia-gui ./cmd/qingqiuxia-gui

run: build
	./bin/qingqiuxia

gui: build
	./bin/qingqiuxia-gui -demo

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
