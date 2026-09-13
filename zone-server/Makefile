.PHONY: build build-linux test run dist

build:
	go build -o bin/zone-server.exe cmd/server/main.go

build-linux:
	GOOS=linux GOARCH=amd64 go build -o bin/zone-server-linux cmd/server/main.go

test:
	go test -v ./...

run: build
	./bin/zone-server.exe

dist: build build-linux
	mkdir -p dist
	cp bin/zone-server.exe dist/
	cp bin/zone-server-linux dist/
	cp zone_server.yaml dist/
