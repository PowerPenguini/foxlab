SHELL := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c
.ONESHELL:

GO ?= go
NPM ?= npm
GOCACHE ?= /tmp/foxlab-go-cache
GOPROXY ?= off
SHELL_ADDR ?= 127.0.0.1:8088
TOPOLOGY_ADDR ?= 127.0.0.1:8090
FILES_ADDR ?= 127.0.0.1:8093
WORKSPACE ?= .
LIBVIRT_URI ?= qemu:///system

.PHONY: start-dev dev dev-all dev-shell dev-topology dev-files dev-terminal package-topology package-vnc-viewer package-terminal package-files macnat-module macnat-module-clean build test

start-dev: package-topology package-vnc-viewer package-terminal package-files dev-shell

dev: start-dev

dev-all: package-topology package-vnc-viewer package-terminal package-files
	@echo "Foxlab dev shell:     http://127.0.0.1:5173"
	@echo "Topology app:         http://$(TOPOLOGY_ADDR)"
	@echo "Files app:            http://$(FILES_ADDR)"
	@echo "Shell API backend:    http://$(SHELL_ADDR)"
	@setsid env GOCACHE="$(GOCACHE)" GOPROXY="$(GOPROXY)" $(GO) run . serve --workspace "$(WORKSPACE)" --addr "$(SHELL_ADDR)" --uri "$(LIBVIRT_URI)" &
	shell_pid=$$!
	setsid env GOCACHE="$(GOCACHE)" GOPROXY="$(GOPROXY)" $(GO) run . app run dist/apps/topology.foxapp --workspace "$(WORKSPACE)" --addr "$(TOPOLOGY_ADDR)" --uri "$(LIBVIRT_URI)" &
	topology_pid=$$!
	setsid env GOCACHE="$(GOCACHE)" GOPROXY="$(GOPROXY)" $(GO) run . app run dist/apps/files.foxapp --workspace "$(WORKSPACE)" --addr "$(FILES_ADDR)" --uri "$(LIBVIRT_URI)" &
	files_pid=$$!
	cd web
	setsid $(NPM) run dev &
	shell_vite_pid=$$!
	cleanup() {
		trap - EXIT INT TERM
		kill -- -$$shell_pid -$$topology_pid -$$files_pid -$$shell_vite_pid >/dev/null 2>&1 || true
		wait $$shell_pid $$topology_pid $$files_pid $$shell_vite_pid >/dev/null 2>&1 || true
	}
	trap cleanup EXIT
	trap 'cleanup; exit 130' INT TERM
	wait -n $$shell_pid $$topology_pid $$files_pid $$shell_vite_pid

dev-shell:
	@echo "Foxlab dev shell:  http://127.0.0.1:5173"
	@echo "Shell API backend: http://$(SHELL_ADDR)"
	@setsid env GOCACHE="$(GOCACHE)" GOPROXY="$(GOPROXY)" $(GO) run . serve --workspace "$(WORKSPACE)" --addr "$(SHELL_ADDR)" --uri "$(LIBVIRT_URI)" &
	shell_pid=$$!
	cd web
	setsid $(NPM) run dev &
	vite_pid=$$!
	cleanup() {
		trap - EXIT INT TERM
		kill -- -$$shell_pid -$$vite_pid >/dev/null 2>&1 || true
		wait $$shell_pid $$vite_pid >/dev/null 2>&1 || true
	}
	trap cleanup EXIT
	trap 'cleanup; exit 130' INT TERM
	wait -n $$shell_pid $$vite_pid

dev-topology: package-topology package-vnc-viewer package-terminal package-files
	@echo "Topology app: http://$(TOPOLOGY_ADDR)"
	@setsid env GOCACHE="$(GOCACHE)" GOPROXY="$(GOPROXY)" $(GO) run . app run dist/apps/topology.foxapp --workspace "$(WORKSPACE)" --addr "$(TOPOLOGY_ADDR)" --uri "$(LIBVIRT_URI)" &
	topology_pid=$$!
	cleanup() {
		trap - EXIT INT TERM
		kill -- -$$topology_pid >/dev/null 2>&1 || true
		wait $$topology_pid >/dev/null 2>&1 || true
	}
	trap cleanup EXIT
	trap 'cleanup; exit 130' INT TERM
	wait $$topology_pid

dev-files: package-files
	@echo "Files app: http://$(FILES_ADDR)"
	@setsid env GOCACHE="$(GOCACHE)" GOPROXY="$(GOPROXY)" $(GO) run . app run dist/apps/files.foxapp --workspace "$(WORKSPACE)" --addr "$(FILES_ADDR)" --uri "$(LIBVIRT_URI)" &
	files_pid=$$!
	cleanup() {
		trap - EXIT INT TERM
		kill -- -$$files_pid >/dev/null 2>&1 || true
		wait $$files_pid >/dev/null 2>&1 || true
	}
	trap cleanup EXIT
	trap 'cleanup; exit 130' INT TERM
	wait $$files_pid

dev-terminal: package-terminal
	@echo "Terminal app: http://127.0.0.1:8092"
	@setsid env GOCACHE="$(GOCACHE)" GOPROXY="$(GOPROXY)" $(GO) run . app run dist/apps/terminal.foxapp --workspace "$(WORKSPACE)" --addr 127.0.0.1:8092 --uri "$(LIBVIRT_URI)" &
	terminal_pid=$$!
	cleanup() {
		trap - EXIT INT TERM
		kill -- -$$terminal_pid >/dev/null 2>&1 || true
		wait $$terminal_pid >/dev/null 2>&1 || true
	}
	trap cleanup EXIT
	trap 'cleanup; exit 130' INT TERM
	wait $$terminal_pid

package-topology:
	mkdir -p apps/topology/bin dist/apps
	GOCACHE="$(GOCACHE)" GOPROXY="$(GOPROXY)" $(GO) build -buildvcs=false -o apps/topology/bin/topology ./apps/topology/cmd/topology
	$(NPM) --prefix apps/topology run build
	GOCACHE="$(GOCACHE)" GOPROXY="$(GOPROXY)" $(GO) run . app package apps/topology --out dist/apps/topology.foxapp

package-vnc-viewer:
	mkdir -p apps/vnc-viewer/bin dist/apps
	GOCACHE="$(GOCACHE)" GOPROXY="$(GOPROXY)" $(GO) build -buildvcs=false -o apps/vnc-viewer/bin/vnc-viewer ./apps/vnc-viewer/cmd/vnc-viewer
	$(NPM) --prefix apps/vnc-viewer run build
	GOCACHE="$(GOCACHE)" GOPROXY="$(GOPROXY)" $(GO) run . app package apps/vnc-viewer --out dist/apps/vnc-viewer.foxapp

package-terminal:
	mkdir -p apps/terminal/bin dist/apps
	GOCACHE="$(GOCACHE)" GOPROXY="$(GOPROXY)" $(GO) build -buildvcs=false -o apps/terminal/bin/terminal ./apps/terminal/cmd/terminal
	$(NPM) --prefix apps/terminal run build
	GOCACHE="$(GOCACHE)" GOPROXY="$(GOPROXY)" $(GO) run . app package apps/terminal --out dist/apps/terminal.foxapp

package-files:
	mkdir -p apps/files/bin dist/apps
	GOCACHE="$(GOCACHE)" GOPROXY="$(GOPROXY)" $(GO) build -buildvcs=false -o apps/files/bin/files ./apps/files/cmd/files
	$(NPM) --prefix apps/files run build
	GOCACHE="$(GOCACHE)" GOPROXY="$(GOPROXY)" $(GO) run . app package apps/files --out dist/apps/files.foxapp

macnat-module:
	$(MAKE) -C drivers/macnat BUILD_DIR="$(CURDIR)/build/macnat"

macnat-module-clean:
	$(MAKE) -C drivers/macnat BUILD_DIR="$(CURDIR)/build/macnat" clean

build: package-topology package-vnc-viewer package-terminal package-files
	GOCACHE="$(GOCACHE)" GOPROXY="$(GOPROXY)" $(GO) build -buildvcs=false ./...
	$(NPM) --prefix web run build

test:
	GOCACHE="$(GOCACHE)" GOPROXY="$(GOPROXY)" $(GO) test ./...
