.PHONY: run build build-linux test vet docker
run:
	go run ./cmd/godexvert
build:
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o bin/godexvert ./cmd/godexvert
build-linux:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o bin/godexvert-linux-amd64 ./cmd/godexvert
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o bin/godexvert-linux-arm64 ./cmd/godexvert
test:
	go test -race ./...
vet:
	go vet ./...
docker:
	docker build -t godexvert .
