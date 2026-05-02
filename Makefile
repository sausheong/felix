BINARY    := felix
CMD       := ./cmd/felix
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT    ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
LDFLAGS   := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)

APPLE_ID         := sausheong@sausheong.com
TEAM_ID          := 83N864XA6Z
APP_SIGN_ID      := Developer ID Application: Sau Sheong Chang (83N864XA6Z)
PKG_SIGN_ID      := Developer ID Installer: Sau Sheong Chang (83N864XA6Z)
KEYCHAIN_PROFILE := felix-notary

.PHONY: build build-app build-app-windows build-small run test test-race test-v lint fmt vet tidy clean snapshot install release publish-release build-release installer installer-windows sign ollama-fetch _payload-secret-scan _payload-secret-scan-windows

## build: compile the binary
build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) $(CMD)

## build-app: compile the menu bar app as a macOS .app bundle
##
## felix-app is now a thin launcher that spawns `felix start` as a
## subprocess (so a macOS Cocoa-level reap of the menubar app does
## not bring down the gateway). Both binaries land in the bundle —
## felix-app at Contents/MacOS/felix-app, felix at
## Contents/Resources/bin/felix (alongside ollama).
build-app: ollama-fetch
	go build -ldflags "$(LDFLAGS)" -o felix-app ./cmd/felix-app
	go build -ldflags "$(LDFLAGS)" -o felix ./cmd/felix
	rm -rf Felix.app
	mkdir -p Felix.app/Contents/MacOS Felix.app/Contents/Resources/bin
	cp felix-app Felix.app/Contents/MacOS/felix-app
	cp felix Felix.app/Contents/Resources/bin/felix
	chmod +x Felix.app/Contents/Resources/bin/felix
	cp cmd/felix-app/Info.plist Felix.app/Contents/Info.plist
	cp cmd/felix-app/icon.icns Felix.app/Contents/Resources/icon.icns
	@if [ -f bin/ollama-darwin-arm64 ]; then \
	  cp bin/ollama-darwin-arm64 Felix.app/Contents/Resources/bin/ollama; \
	  chmod +x Felix.app/Contents/Resources/bin/ollama; \
	fi
	rm -f felix-app felix
	@echo "Built Felix.app"

## build-app-windows: cross-compile the menu bar app + CLI for Windows
##
## felix-app.exe spawns felix.exe as a subprocess at runtime; both
## binaries must ship side-by-side. Place them next to each other in
## the same directory when distributing.
build-app-windows:
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS) -H windowsgui" -o felix-app.exe ./cmd/felix-app
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o felix.exe ./cmd/felix
	@echo "Built felix-app.exe and felix.exe"

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
release: ollama-fetch
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
		case "$$os/$$arch" in \
		  darwin/amd64)  oll="ollama-darwin-amd64";; \
		  darwin/arm64)  oll="ollama-darwin-arm64";; \
		  linux/amd64)   oll="ollama-linux-amd64";; \
		  linux/arm64)   oll="ollama-linux-arm64";; \
		  windows/amd64) oll="ollama-windows-amd64.exe";; \
		esac; \
		if [ -f "bin/$$oll" ]; then \
		  mkdir -p $(RELEASE_DIR)/$$name/bin; \
		  ext2=""; if [ "$$os" = "windows" ]; then ext2=".exe"; fi; \
		  cp bin/$$oll $(RELEASE_DIR)/$$name/bin/ollama$$ext2; \
		fi; \
		(cd $(RELEASE_DIR) && zip -rq $$name.zip $$name); \
		rm -rf $(RELEASE_DIR)/$$name; \
	done
	@if [ "$$(uname)" = "Darwin" ]; then \
		echo "Building Felix.app (macOS menu bar app)..."; \
		arch=$$(uname -m); \
		if [ "$$arch" = "x86_64" ]; then arch="amd64"; fi; \
		go build -ldflags "-s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)" -o $(RELEASE_DIR)/felix-app ./cmd/felix-app; \
		rm -rf $(RELEASE_DIR)/Felix.app; \
		mkdir -p $(RELEASE_DIR)/Felix.app/Contents/MacOS $(RELEASE_DIR)/Felix.app/Contents/Resources; \
		cp $(RELEASE_DIR)/felix-app $(RELEASE_DIR)/Felix.app/Contents/MacOS/felix-app; \
		cp cmd/felix-app/Info.plist $(RELEASE_DIR)/Felix.app/Contents/Info.plist; \
		cp cmd/felix-app/icon.icns $(RELEASE_DIR)/Felix.app/Contents/Resources/icon.icns; \
		rm -f $(RELEASE_DIR)/felix-app; \
		(cd $(RELEASE_DIR) && zip -rq Felix-$(VERSION)-macos-$$arch.zip Felix.app); \
		rm -rf $(RELEASE_DIR)/Felix.app; \
	fi
	@echo "Building Felix tray app for Windows..."
	@CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
		go build -trimpath -ldflags "-s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -H windowsgui" \
		-o $(RELEASE_DIR)/felix-app-$(VERSION)-windows-amd64/felix-app.exe ./cmd/felix-app
	@(cd $(RELEASE_DIR) && zip -rq felix-app-$(VERSION)-windows-amd64.zip felix-app-$(VERSION)-windows-amd64)
	@rm -rf $(RELEASE_DIR)/felix-app-$(VERSION)-windows-amd64
	@echo ""
	@echo "Release artifacts in $(RELEASE_DIR)/:"
	@ls -1 $(RELEASE_DIR)/*.zip

## publish-release: create a GitHub release for the latest tag with notes from
## commits between the previous tag and the latest tag. Attaches any *.zip
## artifacts found in $(RELEASE_DIR)/ (e.g. from a prior `make build-release`).
publish-release:
	@latest=$$(git describe --tags --abbrev=0 2>/dev/null) || { echo "ERROR: no tags found"; exit 1; }; \
	if [ -z "$$latest" ]; then echo "ERROR: no tags found"; exit 1; fi; \
	prev=$$(git describe --tags --abbrev=0 "$$latest^" 2>/dev/null || true); \
	echo "==> Publishing release for $$latest"; \
	notes_file=$$(mktemp); \
	trap "rm -f $$notes_file" EXIT INT TERM; \
	if [ -n "$$prev" ]; then \
	  echo "    Notes from $$prev..$$latest"; \
	  printf '## Changes since %s\n\n' "$$prev" > $$notes_file; \
	  git log "$$prev..$$latest" --pretty=format:'- %s' --no-merges >> $$notes_file; \
	else \
	  echo "    No previous tag — including all commits up to $$latest"; \
	  printf '## Initial release\n\n' > $$notes_file; \
	  git log "$$latest" --pretty=format:'- %s' --no-merges >> $$notes_file; \
	fi; \
	if [ ! -s $$notes_file ]; then echo "No notable changes." > $$notes_file; fi; \
	if gh release view "$$latest" >/dev/null 2>&1; then \
	  echo "ERROR: GitHub release $$latest already exists. Delete first with:"; \
	  echo "  gh release delete $$latest --yes"; \
	  exit 1; \
	fi; \
	gh release create "$$latest" --title "$$latest" --notes-file "$$notes_file" || exit 1; \
	artifacts=$$(ls $(RELEASE_DIR)/*$$latest*.zip $(RELEASE_DIR)/*$$latest*.pkg 2>/dev/null); \
	if [ -n "$$artifacts" ]; then \
	  echo "==> Uploading artifacts for $$latest from $(RELEASE_DIR)/"; \
	  echo "$$artifacts" | sed 's/^/    /'; \
	  gh release upload "$$latest" $$artifacts --clobber; \
	else \
	  echo "    No $$latest artifacts in $(RELEASE_DIR)/ to attach (run 'make build-release' and/or 'make sign' first)"; \
	  if ls $(RELEASE_DIR)/*.zip $(RELEASE_DIR)/*.pkg >/dev/null 2>&1; then \
	    echo "    NOTE: $(RELEASE_DIR)/ contains artifacts from other versions — they were skipped:"; \
	    ls $(RELEASE_DIR)/*.zip $(RELEASE_DIR)/*.pkg 2>/dev/null | sed 's/^/      /'; \
	  fi; \
	fi; \
	echo "==> Released: https://github.com/sausheong/felix/releases/tag/$$latest"

## build-release: cross-compile CLI for all platforms without creating a GitHub release
build-release: ollama-fetch
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
		case "$$os/$$arch" in \
		  darwin/amd64)  oll="ollama-darwin-amd64";; \
		  darwin/arm64)  oll="ollama-darwin-arm64";; \
		  linux/amd64)   oll="ollama-linux-amd64";; \
		  linux/arm64)   oll="ollama-linux-arm64";; \
		  windows/amd64) oll="ollama-windows-amd64.exe";; \
		esac; \
		if [ -f "bin/$$oll" ]; then \
		  mkdir -p $(RELEASE_DIR)/$$name/bin; \
		  ext2=""; if [ "$$os" = "windows" ]; then ext2=".exe"; fi; \
		  cp bin/$$oll $(RELEASE_DIR)/$$name/bin/ollama$$ext2; \
		fi; \
		(cd $(RELEASE_DIR) && zip -rq $$name.zip $$name); \
		rm -rf $(RELEASE_DIR)/$$name; \
	done
	@if [ "$$(uname)" = "Darwin" ]; then \
		echo "Building Felix.app (macOS menu bar app)..."; \
		arch=$$(uname -m); \
		if [ "$$arch" = "x86_64" ]; then arch="amd64"; fi; \
		go build -ldflags "$(LDFLAGS)" -o $(RELEASE_DIR)/felix-app ./cmd/felix-app; \
		rm -rf $(RELEASE_DIR)/Felix.app; \
		mkdir -p $(RELEASE_DIR)/Felix.app/Contents/MacOS $(RELEASE_DIR)/Felix.app/Contents/Resources; \
		cp $(RELEASE_DIR)/felix-app $(RELEASE_DIR)/Felix.app/Contents/MacOS/felix-app; \
		cp cmd/felix-app/Info.plist $(RELEASE_DIR)/Felix.app/Contents/Info.plist; \
		cp cmd/felix-app/icon.icns $(RELEASE_DIR)/Felix.app/Contents/Resources/icon.icns; \
		rm -f $(RELEASE_DIR)/felix-app; \
		(cd $(RELEASE_DIR) && zip -rq Felix-$(VERSION)-macos-$$arch.zip Felix.app); \
		rm -rf $(RELEASE_DIR)/Felix.app; \
	fi
	@echo "Building Felix tray app for Windows..."
	@CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
		go build -trimpath -ldflags "$(LDFLAGS) -H windowsgui" \
		-o $(RELEASE_DIR)/felix-app-$(VERSION)-windows-amd64/felix-app.exe ./cmd/felix-app
	@(cd $(RELEASE_DIR) && zip -rq felix-app-$(VERSION)-windows-amd64.zip felix-app-$(VERSION)-windows-amd64)
	@rm -rf $(RELEASE_DIR)/felix-app-$(VERSION)-windows-amd64
	@echo ""
	@echo "Release artifacts in $(RELEASE_DIR)/:"
	@ls -1 $(RELEASE_DIR)/*.zip

## installer: build a macOS PKG installer with bundled skills and provider setup
installer: build-app
	# Wipe any stale staging area BEFORE building so an aborted previous run
	# (or files dropped in by hand for testing) can't leak into the .pkg.
	rm -rf installer/payload
	mkdir -p installer/payload/Applications
	mkdir -p installer/payload/usr/local/share/felix/skills
	cp -r Felix.app installer/payload/Applications/Felix.app
	@if [ -f bin/ollama-darwin-arm64 ]; then \
	  mkdir -p installer/payload/Applications/Felix.app/Contents/Resources/bin; \
	  cp bin/ollama-darwin-arm64 installer/payload/Applications/Felix.app/Contents/Resources/bin/ollama; \
	fi
	cp skills/*.md installer/payload/usr/local/share/felix/skills/
	@$(MAKE) --no-print-directory _payload-secret-scan
	pkgbuild \
		--root installer/payload \
		--component-plist installer/Felix-component.plist \
		--scripts installer/scripts \
		--identifier com.felix.app \
		--version $(VERSION) \
		--install-location / \
		Felix-component.pkg
	productbuild \
		--package Felix-component.pkg \
		--identifier com.felix.app \
		Felix-$(VERSION).pkg
	rm -rf Felix-component.pkg installer/payload
	@echo "Installer: Felix-$(VERSION).pkg"

## installer-windows: build a Windows .exe installer (Inno Setup) with bundled skills + ollama
##
## Uses the amake/innosetup Docker image — no wine needed on the host.
## Requires Docker Desktop or colima running. First run pulls the image (~200MB).
##
## Override ISCC if you want to point at a different compiler invocation
## (e.g. native iscc.exe when running on Windows):
##   make installer-windows ISCC='iscc.exe'
ISCC ?= docker run --rm -i -v "$$PWD:/work" amake/innosetup

installer-windows: ollama-fetch
	@if [ ! -f bin/ollama-windows-amd64.exe ]; then \
	  echo "ERROR: bin/ollama-windows-amd64.exe missing — run 'make ollama-fetch' first"; exit 1; \
	fi
	# Wipe stale staging area BEFORE building so leftover files can't ship.
	rm -rf installer/windows/payload
	@echo "==> Cross-compiling Felix binaries for windows/amd64..."
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
		go build -trimpath -ldflags "$(LDFLAGS)" \
		-o installer/windows/payload/felix.exe $(CMD)
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
		go build -trimpath -ldflags "$(LDFLAGS) -H windowsgui" \
		-o installer/windows/payload/felix-app.exe ./cmd/felix-app
	@echo "==> Staging payload..."
	mkdir -p installer/windows/payload/bin installer/windows/payload/skills
	cp bin/ollama-windows-amd64.exe installer/windows/payload/bin/ollama.exe
	cp skills/*.md installer/windows/payload/skills/
	@$(MAKE) --no-print-directory _payload-secret-scan-windows
	@if [ ! -f installer/windows/Felix.ico ] && command -v magick >/dev/null 2>&1; then \
	  echo "==> Generating Felix.ico from cmd/felix-app/icon.png..."; \
	  magick cmd/felix-app/icon.png -define icon:auto-resize=256,128,64,48,32,16 installer/windows/Felix.ico; \
	elif [ ! -f installer/windows/Felix.ico ]; then \
	  echo "(skipping installer icon — install ImageMagick or drop installer/windows/Felix.ico to embed one)"; \
	fi
	@echo "==> Compiling installer with Inno Setup..."
	cd installer/windows && $(ISCC) /DMyAppVersion=$(patsubst v%,%,$(VERSION)) Felix.iss
	mv installer/windows/Felix-$(patsubst v%,%,$(VERSION))-windows-amd64.exe .
	rm -rf installer/windows/payload
	@echo "Installer: Felix-$(patsubst v%,%,$(VERSION))-windows-amd64.exe"

## sign: sign, notarize, and staple the macOS PKG installer
sign: build-app
	# Sign the app bundle
	codesign --deep --force --options runtime \
		--sign "$(APP_SIGN_ID)" \
		Felix.app
	# Wipe any stale staging area BEFORE assembling so an aborted previous run
	# (or files dropped in by hand for testing) can't leak into the signed pkg.
	rm -rf installer/payload
	# Assemble payload with signed app
	mkdir -p installer/payload/Applications
	mkdir -p installer/payload/usr/local/share/felix/skills
	cp -r Felix.app installer/payload/Applications/Felix.app
	@if [ -f bin/ollama-darwin-arm64 ]; then \
	  mkdir -p installer/payload/Applications/Felix.app/Contents/Resources/bin; \
	  cp bin/ollama-darwin-arm64 installer/payload/Applications/Felix.app/Contents/Resources/bin/ollama; \
	fi
	cp skills/*.md installer/payload/usr/local/share/felix/skills/
	@$(MAKE) --no-print-directory _payload-secret-scan
	pkgbuild \
		--root installer/payload \
		--component-plist installer/Felix-component.plist \
		--scripts installer/scripts \
		--identifier com.felix.app \
		--version $(VERSION) \
		--install-location / \
		Felix-component.pkg
	# Sign the PKG into the release artifacts directory so publish-release
	# picks it up alongside the cross-compiled zips.
	@mkdir -p $(RELEASE_DIR)
	productsign \
		--sign "$(PKG_SIGN_ID)" \
		Felix-component.pkg \
		$(RELEASE_DIR)/Felix-$(VERSION)-signed.pkg
	rm -rf Felix-component.pkg installer/payload
	# Notarize
	xcrun notarytool submit $(RELEASE_DIR)/Felix-$(VERSION)-signed.pkg \
		--apple-id "$(APPLE_ID)" \
		--team-id "$(TEAM_ID)" \
		--keychain-profile "$(KEYCHAIN_PROFILE)" \
		--wait
	# Staple
	xcrun stapler staple $(RELEASE_DIR)/Felix-$(VERSION)-signed.pkg
	@echo "Signed installer: $(RELEASE_DIR)/Felix-$(VERSION)-signed.pkg"

## _payload-secret-scan: refuse to ship if the staged payload contains user
## config or anything that looks like an API key. Internal target invoked by
## `installer` and `sign` after staging completes; not meant to be run on its
## own. Belt-and-braces against the day someone copies their personal
## ~/.felix/felix.json5 into installer/payload/ for testing and forgets to
## clean it up — the build aborts loudly instead of shipping their key.
_payload-secret-scan:
	@bad=$$(find installer/payload \( -name 'felix.json' -o -name 'felix.json5' \
	    -o -name '.env' -o -name '.env.*' -o -name '*.pem' -o -name '*.key' \
	    -o -name 'id_rsa*' -o -name 'id_ed25519*' \) 2>/dev/null); \
	if [ -n "$$bad" ]; then \
	  echo "ERROR: payload contains forbidden file(s):"; \
	  echo "$$bad" | sed 's/^/  /'; \
	  echo "Remove them before building the installer."; \
	  exit 1; \
	fi; \
	keyhit=$$(grep -rIl -E '(sk-[A-Za-z0-9_-]{20,}|"api_key"[[:space:]]*:[[:space:]]*"[^"]+")' \
	    installer/payload 2>/dev/null); \
	if [ -n "$$keyhit" ]; then \
	  echo "ERROR: payload contains files matching API-key patterns:"; \
	  echo "$$keyhit" | sed 's/^/  /'; \
	  echo "Inspect and remove before shipping."; \
	  exit 1; \
	fi; \
	echo "payload secret-scan: OK"

## _payload-secret-scan-windows: same idea as _payload-secret-scan but
## targets the Windows installer's staging directory.
_payload-secret-scan-windows:
	@bad=$$(find installer/windows/payload \( -name 'felix.json' -o -name 'felix.json5' \
	    -o -name '.env' -o -name '.env.*' -o -name '*.pem' -o -name '*.key' \
	    -o -name 'id_rsa*' -o -name 'id_ed25519*' \) 2>/dev/null); \
	if [ -n "$$bad" ]; then \
	  echo "ERROR: payload contains forbidden file(s):"; \
	  echo "$$bad" | sed 's/^/  /'; \
	  echo "Remove them before building the installer."; \
	  exit 1; \
	fi; \
	keyhit=$$(grep -rIl -E '(sk-[A-Za-z0-9_-]{20,}|"api_key"[[:space:]]*:[[:space:]]*"[^"]+")' \
	    installer/windows/payload 2>/dev/null); \
	if [ -n "$$keyhit" ]; then \
	  echo "ERROR: payload contains files matching API-key patterns:"; \
	  echo "$$keyhit" | sed 's/^/  /'; \
	  echo "Inspect and remove before shipping."; \
	  exit 1; \
	fi; \
	echo "windows payload secret-scan: OK"

## clean: remove build artifacts
clean:
	rm -f $(BINARY) felix-app felix-app.exe
	rm -f Felix-*.pkg Felix-*-windows-amd64.exe
	rm -rf Felix.app $(RELEASE_DIR) installer/windows/payload
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

OLLAMA_VERSION ?= 0.21.0
OLLAMA_BASE_URL := https://github.com/ollama/ollama/releases/download/v$(OLLAMA_VERSION)

## ollama-fetch: download and extract platform Ollama binaries into bin/
##
## Ollama v0.6+ ships archives (not raw binaries). Layouts differ per platform:
##   darwin (universal):  ollama-darwin.tgz      → ollama (FLAT)
##   linux/amd64,arm64:   ollama-linux-*.tar.zst → bin/ollama (NESTED, requires zstd)
##   windows/amd64:       ollama-windows-amd64.zip → ollama.exe (FLAT)
##
## NOTE: We extract only the bare binary. The Linux and Windows archives also
## ship lib/ollama/ runtime libraries (CUDA, ROCm, etc.) used for GPU support;
## the bundled ollama in Felix releases will fall back to CPU-only inference.
## For full GPU support, users should install ollama system-wide.
##
## The macOS darwin binary is universal2 (amd64+arm64); we materialize it under
## both names so per-platform release zips can pick it up uniformly.
## Skip fetch when bin/.ollama-version matches OLLAMA_VERSION and all five
## per-platform binaries are present. To force a re-fetch, delete bin/ or
## bin/.ollama-version (e.g. after bumping OLLAMA_VERSION).
ollama-fetch:
	@if [ -f bin/.ollama-version ] \
	    && [ "$$(cat bin/.ollama-version)" = "$(OLLAMA_VERSION)" ] \
	    && [ -x bin/ollama-darwin-amd64 ] && [ -x bin/ollama-darwin-arm64 ] \
	    && [ -x bin/ollama-linux-amd64 ] && [ -x bin/ollama-linux-arm64 ] \
	    && [ -f bin/ollama-windows-amd64.exe ]; then \
	  echo "ollama $(OLLAMA_VERSION) already pinned in bin/, skipping fetch"; \
	  exit 0; \
	fi; \
	mkdir -p bin && \
	tmp=$$(mktemp -d) && trap "rm -rf $$tmp" EXIT && \
	echo "Fetching ollama-darwin.tgz..." && \
	curl -fL -o $$tmp/ollama-darwin.tgz "$(OLLAMA_BASE_URL)/ollama-darwin.tgz" && \
	tar -xzf $$tmp/ollama-darwin.tgz -C $$tmp ollama && \
	cp $$tmp/ollama bin/ollama-darwin-amd64 && \
	cp $$tmp/ollama bin/ollama-darwin-arm64 && \
	chmod +x bin/ollama-darwin-amd64 bin/ollama-darwin-arm64 && \
	rm -f $$tmp/ollama && \
	for arch in amd64 arm64; do \
	  echo "Fetching ollama-linux-$$arch.tar.zst..."; \
	  curl -fL -o $$tmp/ollama-linux-$$arch.tar.zst "$(OLLAMA_BASE_URL)/ollama-linux-$$arch.tar.zst" || exit 1; \
	  if ! command -v zstd >/dev/null 2>&1; then \
	    echo "ERROR: zstd not found. Install with: brew install zstd"; exit 1; \
	  fi; \
	  zstd -d -c $$tmp/ollama-linux-$$arch.tar.zst | tar -x -C $$tmp bin/ollama || exit 1; \
	  cp $$tmp/bin/ollama bin/ollama-linux-$$arch || exit 1; \
	  chmod +x bin/ollama-linux-$$arch; \
	  rm -rf $$tmp/bin; \
	done && \
	echo "Fetching ollama-windows-amd64.zip..." && \
	curl -fL -o $$tmp/ollama-windows-amd64.zip "$(OLLAMA_BASE_URL)/ollama-windows-amd64.zip" && \
	unzip -o -j -d $$tmp $$tmp/ollama-windows-amd64.zip ollama.exe >/dev/null && \
	cp $$tmp/ollama.exe bin/ollama-windows-amd64.exe && \
	(cd bin && shasum -a 256 ollama-* > ../OLLAMA-SHA256SUMS) && \
	echo "$(OLLAMA_VERSION)" > bin/.ollama-version && \
	echo "Pinned Ollama binaries in bin/, checksums in OLLAMA-SHA256SUMS"
