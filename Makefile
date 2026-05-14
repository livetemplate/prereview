.PHONY: all build test sync-client install clean

BIN := prereview
CLIENT_SRC := ../client/dist/livetemplate-client.browser.js
CLIENT_DST := internal/assets/client/livetemplate-client.browser.js

all: build

sync-client:
	@test -f $(CLIENT_SRC) || (echo "missing $(CLIENT_SRC); run 'npm run build' in ../client first" && exit 1)
	cp $(CLIENT_SRC) $(CLIENT_DST)
	@echo "synced $(CLIENT_DST) ($$(wc -c < $(CLIENT_DST)) bytes)"

build: sync-client
	go build -o $(BIN) .

test:
	go test ./...

install: sync-client
	go install .

clean:
	rm -f $(BIN) $(CLIENT_DST)
