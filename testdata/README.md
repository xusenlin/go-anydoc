# testdata

`stub.wasm` is generated, not committed — run `make stub`.

It is a Go program built for `wasip1/wasm` that implements the same ABI as
`rust/src/main.rs` (stdin in, Markdown out, error kind as exit code) without
converting anything. It exists so the Go harness — stdio wiring, exit-code
mapping, concurrency, cancellation, memory containment — can be tested without
a Rust toolchain in the loop.

Tests that need real conversion output live in `anydoc_real_test.go`, which is
tagged `!anydoc_nowasm` and so compiles only when the real `anydoc.wasm` is in
the tree.
