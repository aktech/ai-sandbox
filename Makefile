.PHONY: build clean proxy-bin proxy-image

BIN := bin/psb
PROXY_IMAGE := ai-sandbox-proxy:latest

build:
	go build -trimpath -ldflags='-s -w' -o $(BIN) .
	@ls -la $(BIN)

proxy-bin:
	go build -trimpath -ldflags='-s -w' -o bin/psb-proxy ./cmd/psb-proxy

proxy-image:
	docker build -f Dockerfile.proxy -t $(PROXY_IMAGE) .

clean:
	rm -f $(BIN) bin/psb-proxy
