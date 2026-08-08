.PHONY: test stub wasm clean

# macOS ships shasum, not sha256sum.
SHA256 := $(shell command -v sha256sum >/dev/null 2>&1 && echo sha256sum || echo 'shasum -a 256')

# Build the ABI-compatible test stub with Go's own wasip1 target. This is what
# the test suite runs against; it is not a converter.
stub:
	GOOS=wasip1 GOARCH=wasm go build -o testdata/stub.wasm ./internal/stub

# Tests run under anydoc_nowasm so the suite works without a real anydoc.wasm
# in the tree, and so the no-embed code path is exercised on every run.
test: stub
	go test -race -tags anydoc_nowasm ./...

# Requires Rust 1.88 + wasm32-wasip1. Normally done by the build-wasm workflow.
wasm:
	cargo build --release --target wasm32-wasip1 --manifest-path rust/Cargo.toml
	wasm-opt -Oz --enable-bulk-memory --enable-nontrapping-float-to-int \
		-o anydoc.wasm rust/target/wasm32-wasip1/release/anydoc-wasi.wasm
	$(SHA256) anydoc.wasm > anydoc.wasm.sha256

clean:
	rm -f testdata/stub.wasm
	-cargo clean --manifest-path rust/Cargo.toml
