.PHONY: proto test test-integration tidy lint build-all build-linux build-darwin build-windows clean

PROTO_DIR := pkg/proto/corelink/v1
GEN_DIR := pkg/proto/gen

proto:
	protoc \
	  --go_out=$(GEN_DIR) --go_opt=paths=source_relative \
	  --go-grpc_out=$(GEN_DIR) --go-grpc_opt=paths=source_relative \
	  -I $(PROTO_DIR) \
	  $(PROTO_DIR)/*.proto

test:
	go test ./...

test-integration:
	go test -tags=integration ./...

tidy:
	go mod tidy

lint:
	go vet ./...

# 交叉编译
DIST := dist

build-all: build-linux build-darwin build-windows

build-linux:
	GOOS=linux GOARCH=amd64 go build -o $(DIST)/corelink-linux-amd64 ./cmd/corelink-node
	GOOS=linux GOARCH=amd64 go build -o $(DIST)/corelink-controller-linux-amd64 ./cmd/corelink-controller

build-darwin:
	GOOS=darwin GOARCH=amd64 go build -o $(DIST)/corelink-darwin-amd64 ./cmd/corelink-node
	GOOS=darwin GOARCH=arm64 go build -o $(DIST)/corelink-darwin-arm64 ./cmd/corelink-node
	GOOS=darwin GOARCH=amd64 go build -o $(DIST)/corelink-controller-darwin-amd64 ./cmd/corelink-controller
	GOOS=darwin GOARCH=arm64 go build -o $(DIST)/corelink-controller-darwin-arm64 ./cmd/corelink-controller

build-windows:
	GOOS=windows GOARCH=amd64 go build -o $(DIST)/corelink-windows-amd64.exe ./cmd/corelink-node
	GOOS=windows GOARCH=amd64 go build -o $(DIST)/corelink-controller-windows-amd64.exe ./cmd/corelink-controller

clean:
	rm -rf $(DIST)
