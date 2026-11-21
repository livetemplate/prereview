.PHONY: all build test tmpl-check sync-client build-handlers test-handlers install clean screenshots gifs

BIN := prereview
# The client is vendored from the @livetemplate/client version pinned in
# handlers/package.json, the same pin the handlers compile against. To vendor
# an unreleased local build instead: make sync-client CLIENT_DIR=../client
HANDLERS := handlers
CLIENT_DIR ?= $(HANDLERS)/node_modules/@livetemplate/client
CLIENT_JS_SRC := $(CLIENT_DIR)/dist/livetemplate-client.browser.js
CLIENT_JS_DST := internal/assets/client/livetemplate-client.browser.js
CLIENT_CSS_SRC := $(CLIENT_DIR)/livetemplate.css
CLIENT_CSS_DST := internal/assets/client/livetemplate.css

all: build

$(HANDLERS)/node_modules: $(HANDLERS)/package-lock.json
	cd $(HANDLERS) && npm ci

# prereview's attribute handlers (handlers/src), built into
# internal/assets/handlers/prereview-handlers.js and committed like the client.
build-handlers: $(HANDLERS)/node_modules
	cd $(HANDLERS) && npm run build

test-handlers: $(HANDLERS)/node_modules
	cd $(HANDLERS) && npm test

sync-client: $(HANDLERS)/node_modules
	@test -f $(CLIENT_JS_SRC) || (echo "missing $(CLIENT_JS_SRC)" && exit 1)
	@test -f $(CLIENT_CSS_SRC) || (echo "missing $(CLIENT_CSS_SRC)" && exit 1)
	cp $(CLIENT_JS_SRC) $(CLIENT_JS_DST)
	cp $(CLIENT_CSS_SRC) $(CLIENT_CSS_DST)
	@echo "synced $(CLIENT_JS_DST) ($$(wc -c < $(CLIENT_JS_DST)) bytes)"
	@echo "synced $(CLIENT_CSS_DST) ($$(wc -c < $(CLIENT_CSS_DST)) bytes)"

build: sync-client build-handlers
	go build -o $(BIN) .

test: test-handlers
	go test ./...

# the templates/ set is whitespace-significant — no reflow formatter can touch it
# safely (see CLAUDE.md). This guards a hand-edit: green = rendered output is
# unchanged (safe reformat); red = the edit changed what the browser renders.
# After an intentional content change, regenerate the golden:
#   go test -run TestTemplateOutputSignature -update-sig .
tmpl-check:
	go test -run TestTemplateOutputSignature -count=1 .

install: sync-client build-handlers
	go install .

clean:
	rm -f $(BIN) $(CLIENT_JS_DST) $(CLIENT_CSS_DST)

# Regenerate the captioned README screenshots in docs/ (needs chromium).
screenshots:
	bash cmd/screenshot/capture-readme.sh

# Regenerate the animated README GIFs in docs/ (needs chromium; pure-Go encode).
gifs:
	bash cmd/screenshot/capture-gifs.sh
