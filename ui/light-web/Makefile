.PHONY: build run clean

# Default variables
PORT ?= 10000
BACKEND_PORT ?= 18790
BACKEND_HOST ?= localhost

# Installation
install:
	pnpm install

# Build the production bundle
build: install
	pnpm run build

# Run the development server
run: install
	VITE_BACKEND_PORT=$(BACKEND_PORT) VITE_BACKEND_HOST=$(BACKEND_HOST) pnpm run dev --port $(PORT)

# Clean build artifacts
clean:
	rm -rf dist node_modules
