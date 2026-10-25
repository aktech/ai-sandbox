.PHONY: build clean proxy-bin proxy-image

BIN := bin/aisb
PROXY_IMAGE := ai-sandbox-proxy:latest

build:
	go build -trimpath -ldflags='-s -w' -o $(BIN) .
	@ls -la $(BIN)

proxy-bin:
	go build -trimpath -ldflags='-s -w' -o bin/aisb-proxy ./cmd/aisb-proxy

proxy-image:
	docker build -f Dockerfile.proxy -t $(PROXY_IMAGE) .

clean:
	rm -f $(BIN) bin/aisb-proxy
