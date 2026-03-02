BINARY    := goclaw
CMD       := ./cmd/goclaw
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT    ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
LDFLAGS   := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)

.PHONY: build build-app build-small run test test-race test-v lint fmt vet tidy clean snapshot install release build-release

## build: compile the binary
build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) $(CMD)

## build-app: compile the menu bar app as a macOS .app bundle
build-app:
	go build -ldflags "$(LDFLAGS)" -o goclaw-app ./cmd/goclaw-app
	rm -rf GoClaw.app
	mkdir -p GoClaw.app/Contents/MacOS GoClaw.app/Contents/Resources
	cp goclaw-app GoClaw.app/Contents/MacOS/goclaw-app
	cp cmd/goclaw-app/Info.plist GoClaw.app/Contents/Info.plist
	cp cmd/goclaw-app/icon.icns GoClaw.app/Contents/Resources/icon.icns
	rm -f goclaw-app
	@echo "Built GoClaw.app"

## build-small: compile a smaller, statically-linked binary (+ UPX on Linux)
build-small:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) $(CMD)
	@if [ "$$(uname)" = "Linux" ] && command -v upx >/dev/null 2>&1; then \
		upx --best --lzma $(BINARY); \
	elif [ "$$(uname)" = "Linux" ]; then \
		echo "tip: install upx for further compression (apt install upx)"; \
	fi

## run: build and start the gateway
run: build
	./$(BINARY) start

## test: run all tests
test:
	go test ./...

## test-race: run tests with race detector
test-race:
	go test -race ./...

## test-v: run tests with verbose output
test-v:
	go test -v ./...

## lint: run golangci-lint
lint:
	golangci-lint run

## fmt: format all Go source files
fmt:
	go fmt ./...

## vet: run go vet
vet:
	go vet ./...

## tidy: tidy and verify module dependencies
tidy:
	go mod tidy
	go mod verify

RELEASE_DIR := dist
PLATFORMS   := darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64

## release: commit, push, create GitHub release, and build binaries (usage: make release TAG=v0.1.4)
release:
ifndef TAG
	$(error TAG is required. Usage: make release TAG=v0.1.4)
endif
	@echo "==> Committing and pushing..."
	git add -A
	@if git diff --cached --quiet; then \
		echo "Nothing to commit, working tree clean."; \
	else \
		git commit -m "Release $(TAG)"; \
	fi
	git push
	@echo ""
	@echo "==> Creating GitHub release $(TAG)..."
	$(eval PREV_TAG := $(shell git describe --tags --abbrev=0 2>/dev/null || echo ""))
	$(eval RELEASE_NOTES := $(shell \
		if [ -n "$(PREV_TAG)" ]; then \
			git log $(PREV_TAG)..HEAD --pretty=format:'- %s' --no-merges; \
		else \
			git log --pretty=format:'- %s' --no-merges -20; \
		fi \
	))
	gh release create $(TAG) --title "$(TAG)" --notes "$(RELEASE_NOTES)"
	git fetch --tags
	@echo ""
	@echo "==> Building release binaries..."
	$(eval VERSION := $(TAG))
	rm -rf $(RELEASE_DIR)
	@for platform in $(PLATFORMS); do \
		os=$${platform%%/*}; \
		arch=$${platform##*/}; \
		name=$(BINARY)-$(VERSION)-$${os}-$${arch}; \
		ext=""; \
		if [ "$$os" = "windows" ]; then ext=".exe"; fi; \
		echo "Building $$name..."; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch \
			go build -trimpath -ldflags "-s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)" \
			-o $(RELEASE_DIR)/$$name/$(BINARY)$$ext $(CMD) || exit 1; \
		(cd $(RELEASE_DIR) && zip -rq $$name.zip $$name); \
		rm -rf $(RELEASE_DIR)/$$name; \
	done
	@if [ "$$(uname)" = "Darwin" ]; then \
		echo "Building GoClaw.app (macOS menu bar app)..."; \
		arch=$$(uname -m); \
		if [ "$$arch" = "x86_64" ]; then arch="amd64"; fi; \
		go build -ldflags "-s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)" -o $(RELEASE_DIR)/goclaw-app ./cmd/goclaw-app; \
		rm -rf $(RELEASE_DIR)/GoClaw.app; \
		mkdir -p $(RELEASE_DIR)/GoClaw.app/Contents/MacOS $(RELEASE_DIR)/GoClaw.app/Contents/Resources; \
		cp $(RELEASE_DIR)/goclaw-app $(RELEASE_DIR)/GoClaw.app/Contents/MacOS/goclaw-app; \
		cp cmd/goclaw-app/Info.plist $(RELEASE_DIR)/GoClaw.app/Contents/Info.plist; \
		cp cmd/goclaw-app/icon.icns $(RELEASE_DIR)/GoClaw.app/Contents/Resources/icon.icns; \
		rm -f $(RELEASE_DIR)/goclaw-app; \
		(cd $(RELEASE_DIR) && zip -rq GoClaw-$(VERSION)-macos-$$arch.zip GoClaw.app); \
		rm -rf $(RELEASE_DIR)/GoClaw.app; \
	fi
	@echo ""
	@echo "Release artifacts in $(RELEASE_DIR)/:"
	@ls -1 $(RELEASE_DIR)/*.zip

## build-release: cross-compile CLI for all platforms without creating a GitHub release
build-release:
	rm -rf $(RELEASE_DIR)
	@for platform in $(PLATFORMS); do \
		os=$${platform%%/*}; \
		arch=$${platform##*/}; \
		name=$(BINARY)-$(VERSION)-$${os}-$${arch}; \
		ext=""; \
		if [ "$$os" = "windows" ]; then ext=".exe"; fi; \
		echo "Building $$name..."; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch \
			go build -trimpath -ldflags "$(LDFLAGS)" \
			-o $(RELEASE_DIR)/$$name/$(BINARY)$$ext $(CMD) || exit 1; \
		(cd $(RELEASE_DIR) && zip -rq $$name.zip $$name); \
		rm -rf $(RELEASE_DIR)/$$name; \
	done
	@if [ "$$(uname)" = "Darwin" ]; then \
		echo "Building GoClaw.app (macOS menu bar app)..."; \
		arch=$$(uname -m); \
		if [ "$$arch" = "x86_64" ]; then arch="amd64"; fi; \
		go build -ldflags "$(LDFLAGS)" -o $(RELEASE_DIR)/goclaw-app ./cmd/goclaw-app; \
		rm -rf $(RELEASE_DIR)/GoClaw.app; \
		mkdir -p $(RELEASE_DIR)/GoClaw.app/Contents/MacOS $(RELEASE_DIR)/GoClaw.app/Contents/Resources; \
		cp $(RELEASE_DIR)/goclaw-app $(RELEASE_DIR)/GoClaw.app/Contents/MacOS/goclaw-app; \
		cp cmd/goclaw-app/Info.plist $(RELEASE_DIR)/GoClaw.app/Contents/Info.plist; \
		cp cmd/goclaw-app/icon.icns $(RELEASE_DIR)/GoClaw.app/Contents/Resources/icon.icns; \
		rm -f $(RELEASE_DIR)/goclaw-app; \
		(cd $(RELEASE_DIR) && zip -rq GoClaw-$(VERSION)-macos-$$arch.zip GoClaw.app); \
		rm -rf $(RELEASE_DIR)/GoClaw.app; \
	fi
	@echo ""
	@echo "Release artifacts in $(RELEASE_DIR)/:"
	@ls -1 $(RELEASE_DIR)/*.zip

## clean: remove build artifacts
clean:
	rm -f $(BINARY) goclaw-app
	rm -rf GoClaw.app $(RELEASE_DIR)
	go clean

## snapshot: cross-platform build via goreleaser
snapshot:
	goreleaser build --snapshot --clean

## install: install the binary to $GOPATH/bin
install:
	go install -ldflags "$(LDFLAGS)" $(CMD)

## help: show this help
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## //' | column -t -s ':'
