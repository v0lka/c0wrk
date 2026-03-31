.PHONY: build test lint dev-desktop

build:
	wails build

test:
	go test ./...

lint:
	golangci-lint run

dev-desktop:
	cd frontend && npm run dev
