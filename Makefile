.PHONY: build test vet run clean

build:
	go build -o bin/redproxy ./cmd/redproxy
	go build -o bin/demo-backend ./cmd/demo-backend

test:
	go test ./...

vet:
	go vet ./...

run: build
	./bin/redproxy -config config.example.yaml

clean:
	rm -rf bin
