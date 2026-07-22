BIN := codegraph
PKG := ./cmd/codegraph
PREFIX ?= /usr/local

.PHONY: build test tidy install install-agents run-index run-mcp clean

build:
	go build -o $(BIN) $(PKG)

test:
	go test ./...

tidy:
	go mod tidy

# Install the fork binary to PREFIX/bin (default /usr/local/bin).
# Requires write access (often: sudo make install).
install: build
	install -d $(PREFIX)/bin
	install -m 755 $(BIN) $(PREFIX)/bin/$(BIN)
	@echo "installed $(PREFIX)/bin/$(BIN)"
	@echo "run '$(PREFIX)/bin/$(BIN) install' (or make install-agents) to re-register MCP agents"

# Point detected agents (Claude/Codex/opencode/Grok) at the installed binary.
install-agents: install
	$(PREFIX)/bin/$(BIN) install

# make run-index REPO=/path/to/repo
run-index: build
	./$(BIN) index $(REPO)

# make run-mcp REPO=/path/to/repo  (omit REPO to use cwd / CLAUDE_PROJECT_DIR)
run-mcp: build
	./$(BIN) mcp $(REPO)

clean:
	rm -f $(BIN) $(BIN).exe
