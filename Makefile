.PHONY: all build test sync-client install clean screenshots gifs

BIN := prereview
CLIENT_JS_SRC := ../client/dist/livetemplate-client.browser.js
CLIENT_JS_DST := internal/assets/client/livetemplate-client.browser.js
CLIENT_CSS_SRC := ../client/livetemplate.css
CLIENT_CSS_DST := internal/assets/client/livetemplate.css

all: build

sync-client:
	@test -f $(CLIENT_JS_SRC) || (echo "missing $(CLIENT_JS_SRC); run 'npm run build' in ../client first" && exit 1)
	@test -f $(CLIENT_CSS_SRC) || (echo "missing $(CLIENT_CSS_SRC)" && exit 1)
	cp $(CLIENT_JS_SRC) $(CLIENT_JS_DST)
	cp $(CLIENT_CSS_SRC) $(CLIENT_CSS_DST)
	@echo "synced $(CLIENT_JS_DST) ($$(wc -c < $(CLIENT_JS_DST)) bytes)"
	@echo "synced $(CLIENT_CSS_DST) ($$(wc -c < $(CLIENT_CSS_DST)) bytes)"

build: sync-client
	go build -o $(BIN) .

test:
	go test ./...

install: sync-client
	go install .

clean:
	rm -f $(BIN) $(CLIENT_JS_DST) $(CLIENT_CSS_DST)

# Regenerate the captioned README screenshots in docs/ (needs chromium).
screenshots:
	bash cmd/screenshot/capture-readme.sh

# Regenerate the animated README GIFs in docs/ (needs chromium; pure-Go encode).
gifs:
	bash cmd/screenshot/capture-gifs.sh
