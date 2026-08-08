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

# Build and install $(BIN) to $(PREFIX)/bin (default /usr/local/bin).
# Safe flow: build, stage a temp file *inside* $(PREFIX)/bin, chmod 755,
# verify the staged temp is byte-identical to the freshly built binary (cmp)
# and executable (test -x), then atomically rename it over the destination.
# Validation happens BEFORE the rename; the rename is the last fallible step.
# The destination is resolved up front and rejected when it already exists as
# a directory or a symlink to one — `mv file dir` would otherwise silently
# move the temp *into* the directory and report false success — so the failure
# aborts before any temp is created. Any build, permission, or validation
# failure aborts before the swap, so the previously installed binary is never
# left partially written. The temp file is removed on success and on failure,
# including signals (POSIX traps on 0/HUP/INT/TERM plus `set -eu` — no
# `trap EXIT`), and only the temp we created is ever cleaned.
# Requires write access to $(PREFIX)/bin — typically: sudo make install.
# Override PREFIX for user-local installs, e.g. make install PREFIX=$HOME/.local.
install: build
	@set -eu; \
	prefix_bin=$(PREFIX)/bin; \
	dest="$$prefix_bin/$(BIN)"; \
	mkdir -p "$$prefix_bin"; \
	if [ -d "$$dest" ]; then \
		echo "error: $$dest already exists as a directory (or a symlink to one); refusing to replace it with the $(BIN) binary" >&2; \
		exit 1; \
	fi; \
	tmp=$$(mktemp "$$prefix_bin/.$(BIN).XXXXXX"); \
	trap 'rm -f "$${tmp:-}"' 0 HUP INT TERM; \
	install -m 755 $(BIN) "$$tmp"; \
	cmp -s $(BIN) "$$tmp"; \
	test -x "$$tmp"; \
	mv -f "$$tmp" "$$dest"
	@echo "installed $(PREFIX)/bin/$(BIN)" || true
	@echo "" || true
	@echo "next steps (separate, user-scoped — run as your normal user, not sudo):" || true
	@echo "  1. make install-agents    (use the same PREFIX you installed with)" || true
	@echo "  2. restart OpenCode after agent registration" || true
	@echo "make install does not edit agent configs and does not start MCP." || true

# Point detected agents (Claude/Codex/opencode/Grok) at the installed binary.
# Does NOT rebuild or reinstall: only checks that $(PREFIX)/bin/$(BIN) is
# already installed and executable, then runs it. User-scoped: run as your
# normal user, never under sudo. Use the same PREFIX you used for make install
# (omit it for the default /usr/local).
install-agents:
	@test -x $(PREFIX)/bin/$(BIN) || { echo "error: $(PREFIX)/bin/$(BIN) not installed or not executable — run 'make install' first" >&2; exit 1; }
	$(PREFIX)/bin/$(BIN) install

# make run-index REPO=/path/to/repo
run-index: build
	./$(BIN) index $(REPO)

# make run-mcp REPO=/path/to/repo  (omit REPO to use cwd / CLAUDE_PROJECT_DIR)
run-mcp: build
	./$(BIN) mcp $(REPO)

clean:
	rm -f $(BIN) $(BIN).exe
