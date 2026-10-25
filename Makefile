.PHONY: build clean proxy-bin

BIN := bin/aisb

build:
	go build -trimpath -ldflags='-s -w' -o $(BIN) .
	@ls -la $(BIN)

proxy-bin:
	go build -trimpath -ldflags='-s -w' -o bin/aisb-proxy ./cmd/aisb-proxy

clean:
	rm -f $(BIN) bin/aisb-proxy
