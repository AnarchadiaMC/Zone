.PHONY: build build-linux test run clean dist

build:
	go build -o zone-server.exe cmd/server/main.go

build-linux:
	GOOS=linux GOARCH=amd64 go build -o zone-server-linux cmd/server/main.go

test:
	go test -v ./...

run: build
	./zone-server.exe

clean:
	rm -f zone-server.exe zone-server-linux bin/zone-server.exe bin/zone-server-linux
	rm -rf dist

dist: build build-linux
	mkdir -p dist
	cp zone-server.exe dist/
	cp zone-server-linux dist/
	cp zone_server.yaml dist/
