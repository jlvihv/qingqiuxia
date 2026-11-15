.PHONY: build test race bench e2e lint fmt run clean

build:
	go build -o bin/qingqiuxia ./cmd/qingqiuxia

run: build
	./bin/qingqiuxia

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
