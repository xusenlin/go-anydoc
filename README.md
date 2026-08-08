# go-anydoc

English | [简体中文](README.zh-CN.md)

Go bindings for [anydoc](https://github.com/firecrawl/anydoc) — convert Word,
PowerPoint, Excel, OpenDocument, RTF, EPUB, CSV and text-based PDF to Markdown.

**No cgo. No subprocesses. No system dependencies.** The Rust library is
compiled to WebAssembly and embedded, so a binary using this package is
self-contained and cross-compiles wherever Go does:

```
CGO_ENABLED=0 GOOS=windows GOARCH=arm64 go build ./...
```

## Install

```
go get github.com/xusenlin/go-anydoc
```

## Usage

```go
c, err := anydoc.New()
if err != nil {
    return err
}
defer c.Close(ctx)

md, err := c.Convert(ctx, docBytes, "docx")
```

Pass `""` as the hint to detect the format from content. CSV has no signature
and must be named explicitly.

Errors are matched with `errors.Is`:

```go
switch {
case errors.Is(err, anydoc.ErrEncrypted):    // password-protected
case errors.Is(err, anydoc.ErrUnsupported):  // not a format anydoc parses
case errors.Is(err, anydoc.ErrMalformed):    // recognised but corrupt
}
```

## Design notes

**Interpreter, not the optimising compiler.** wazero can either translate the
module to native machine code up front (fast to run, expensive to load, amd64
and arm64 only) or interpret it instruction by instruction (cheap to load,
portable everywhere, slower to run). This package interprets by default,
because a library that ships inside other people's binaries cannot assume it
may pre-warm a compilation cache, cannot assume the host allows the executable
pages the compiler needs — macOS hardened runtime and some seccomp profiles
refuse them — and cannot assume the target is amd64 or arm64. `WithCompiler`
opts into the other side of that trade.

The trade is real and it scales with document size, so measure against your own
corpus before assuming it is free:

| | interpreter (default) | compiler (`WithCompiler`) |
|---|---|---|
| `New()` — once per process | ~85 ms | ~2.1 s |
| 1 KB docx | 5.0 ms | 1.0 ms |
| docx with a 5 MB uncompressed body | 16.9 s | 1.1 s |
| RSS after `New()` | 135 MB | 576 MB |

Ordinary office documents are in the second row's territory and cost nothing
worth optimising. Multi-megabyte ones are ~16× slower than they would be
compiled, so if you convert those, either bound the tail with
`WithMaxInputBytes` and a context deadline — cancellation interrupts the guest
mid-conversion — or opt into `WithCompiler`.

<sub>Measured on Apple M5 Pro, macOS 26.5, Go 1.26.1, wazero v1.9.0, against
`anydoc.wasm` 5,069,778 bytes (anydoc 0.1.7). The 5 MB figure is a 25,000-row
table: 42 KB zipped, since the size that matters is the uncompressed body.</sub>

**A fresh guest per document.** Each `Convert` instantiates its own linear
memory, so a large document cannot leave memory permanently claimed and a
malformed one cannot leak state into the next call. Compilation, the expensive
part, happens once in `New`.

**A command module, not exported functions.** The guest reads stdin and writes
stdout, so the Go side never touches linear memory, pointers, or UTF-8
boundaries. The exit code carries the error kind. See `rust/src/main.rs` — the
code table there and in `errors.go` is a two-language contract.

**Sandboxed.** WASI is instantiated for stdio and `random_get` (which `HashMap`
seeding reaches for). No filesystem, no clock, no sockets.

## Controlling size

The module is embedded by default so `go get` and `New()` just work. Builds that
would rather ship it out of band:

```
go build -tags anydoc_nowasm    # 4.87 MB smaller
```

`embeddedWASM` is then nil and `New` requires `WithWASM(r)` or
`WithWASMBytes(b)`. Useful for container layering, serverless size limits,
pinning a different anydoc build, or environments that forbid opaque embedded
blobs.

The saving is the 4.83 MB module plus ~35 KB of `embed` machinery. For scale,
`examples/convert` is 12.4 MB built normally and 7.3 MB with the tag.

The build tag affects the compiled binary, not `go get`: the module is in the
Go module either way.

## Tuning

```go
anydoc.New(
    anydoc.WithConcurrency(4),        // simultaneous guests; the main memory lever
    anydoc.WithMemoryLimitPages(512), // 32 MiB per guest
    anydoc.WithMaxInputBytes(64<<20),
    anydoc.WithCompiler(),            // throughput over startup cost; see below
)
```

### `WithCompiler`

Compiles the module to native code instead of interpreting it, turning the
table above from the left column into the right one. The cost is paid **once,
in `New`** — `Convert` only instantiates the already-compiled module — so it
pays off in a long-lived process that reuses one `Converter`, and is a pure
loss in a short-lived one that converts a single small document and exits
(~2.1 s bought to save ~4 ms).

It is a request, not a guarantee. The backend needs amd64 or arm64 on a
mainstream OS, plus a host that permits mmap'd executable pages. Where that
does not hold — riscv64, ppc64le, 386, macOS hardened runtime, some seccomp
profiles — wazero falls back to the interpreter silently and the conversion
still happens, just at interpreter speed. Cross-compilation is unaffected
either way: wazero is pure Go, and this option changes no build constraints.

`WithMemoryLimitPages` is validated against the module's declared minimum at
`New` time, not at conversion time — setting it too low fails fast with a clear
message rather than surfacing later as a mysterious conversion error.

How much a guest actually needs tracks the *uncompressed* document body, at
roughly 15–30× its size — a zip bomb is small on disk and large in memory,
which is why the limit exists:

| document | pages needed |
|---|---|
| module minimum, converts nothing | 64 (4 MiB) |
| docx, 0.4 MB body | 192 (12 MiB) |
| docx, 2 MB body | 576 (36 MiB) |
| docx, 5 MB body | 1280 (80 MiB) |

The default of 512 pages covers a body of roughly 2 MB, which is most office
documents. Raise it if conversions fail with `ErrWASM` on inputs that convert
fine elsewhere; each concurrent guest can claim up to this much, so the product
of `WithConcurrency` and this value is your real memory ceiling.

## Development

```
make test    # builds the wasip1 test stub, runs the suite
make wasm    # rebuilds the real module; needs Rust 1.88 + wasm32-wasip1
```

There are two suites, split by build tag:

- `anydoc_test.go` runs under `-tags anydoc_nowasm` against a stub built with
  Go's own `wasip1` target. The stub speaks the same ABI without converting
  anything, so the harness — stdio wiring, exit-code mapping, concurrency,
  cancellation, memory containment — is testable with no Rust toolchain in the
  loop. See `testdata/README.md`.
- `anydoc_real_test.go` is tagged `!anydoc_nowasm`, so it compiles exactly when
  `anydoc.wasm` is in the tree, and covers what the harness is for: real
  conversion output, format detection, and the error codes the crate actually
  emits.

`anydoc.wasm` is a build artifact committed to the repo, because Go modules
have no build step: whatever is committed is what `go get` delivers. It is
built from the pinned crate by `make wasm` or the `build-wasm` workflow, and
`anydoc.wasm.sha256` records the checksum of the committed copy.

## Versioning

`anydoc.EmbeddedAnydocVersion` reports the upstream crate version this module
was built from; `anydoc.Info()` prints it with the payload size. The crate is
pinned with `=` in `rust/Cargo.toml`: bumping it changes conversion output for
every downstream user, so it goes through the `build-wasm` workflow and a
reviewed PR.

`rust/Cargo.lock` is committed too. The `=` pin only fixes anydoc itself; the
lockfile is what stops a transitive dependency from silently changing the
conversion output between one rebuild and the next.

## License

MIT for this package — see `LICENSE`. The embedded module is built from
[anydoc](https://github.com/firecrawl/anydoc), also MIT — see `LICENSE-anydoc`.
