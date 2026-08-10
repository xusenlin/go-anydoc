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

Requires Go 1.25 or newer, which is what wazero requires.

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
| `New()` — once per process | ~100 ms | ~2.7 s |
| 1 KB docx | 3.5 ms | 0.4 ms |
| docx with a 5 MB uncompressed body | 11.1 s | 0.86 s |
| 7.5 MB PDF | 41.4 s | 3.4 s |
| RSS after `New()` | 182 MB | 638 MB |

Ordinary office documents are in the second row's territory and cost nothing
worth optimising. Multi-megabyte ones are ~12× slower than they would be
compiled, so if you convert those, either bound the tail with
`WithMaxInputBytes` and a context deadline — cancellation interrupts the guest
mid-conversion — or opt into `WithCompiler`.

**The slowness is the runtime, not wasm.** It is tempting to read the numbers
above as the cost of compiling to WebAssembly. Measured on the same PDF and the
same `anydoc.wasm`, it is not — the format costs almost nothing, and nearly all
of the gap is how a particular runtime turns wasm into machine code:

| running the same 7.5 MB PDF | | vs native |
|---|---|---|
| native Rust binary | 0.28 s | 1× |
| the same wasm, wasmtime with Cranelift | 0.37 s | 1.3× |
| the same wasm, wasmtime with Winch, a deliberately baseline compiler | 0.90 s | 3.2× |
| the same wasm, wazy with `WithCompiler` (see below) | 0.75 s | 2.7× |
| the same wasm, wazero with `WithCompiler` | 3.4 s | 12× |
| the same wasm, wazy interpreted (see below) | 33 s | 118× |
| the same wasm, wazero interpreted | 41 s | 146× |

So the things usually blamed — 32-bit pointers, bounds-checked linear memory,
no native SIMD — account for the 1.3×. Everything beyond that is code
generation. wazero's compiler is fast, simple and portable rather than
optimising, which is precisely what buys pure Go with no cgo and no
platform-specific backend to ship.

That is the trade, stated plainly: wasmtime's Go bindings are cgo, so adopting
them would cost `CGO_ENABLED=0`, free cross-compilation, and the single
self-contained binary. If your workload values native speed above those, a cgo
binding is the honest answer and this package is the wrong tool.

### The `experiment-wazy` branch

Most of that gap is not the price of staying in pure Go.
[wazy](https://github.com/samyfodil/wazy) is a pure-Go runtime descended from
wazero that spends its effort on the memory-access paths this workload lives
in, and the [`experiment-wazy`](../../tree/experiment-wazy) branch is this
package running on it — same API, same embedded module, same exit-code ABI,
one import changed:

| converting | `main` (wazero) | `experiment-wazy` | |
|---|---|---|---|
| 1 KB docx, interpreted | 3.5 ms | 2.7 ms | 1.3× |
| 1 KB docx, compiled | 0.4 ms | 0.62 ms | 0.6× |
| 5 MB docx body, interpreted | 11.1 s | 8.6 s | 1.3× |
| 5 MB docx body, compiled | 0.86 s | 0.19 s | 4.5× |
| 7.5 MB PDF, interpreted | 41.4 s | 32.6 s | 1.3× |
| 7.5 MB PDF, compiled | 3.4 s | 0.75 s | 4.5× |

Output is byte-identical, the whole suite passes, and it still cross-compiles
to riscv64, ppc64le, 386 and s390x. Long compute is where it wins; tiny
documents are a wash or slightly worse.

To use it, ask for the branch by name — Go resolves it to a pseudo-version:

```bash
go get github.com/xusenlin/go-anydoc@v0.1.3-experiment.1
```

Nothing else changes: same import path, same API. The tag is a semver
pre-release, so Go skips it for `@latest` and for `go get -u` — it has to be
asked for by name, and it will not move underneath you once it is in `go.mod`.

It is a branch rather than an option because of what the alternatives cost.
Selecting a runtime at call time links both engines into every binary,
measured at **+3.22 MB** for users who only ever use one. A build tag avoids
that, but either way wazy lands in every downstream module graph — and wazy
has no stable release: retracted betas, a pseudo-version, one author, and an
explicit statement that it makes no stability promise. `main` stays on wazero
so nobody inherits that by accident. Take the branch if you want the speed and
can carry the risk; it will be revisited if wazy reaches a stable release.

<sub>Measured on Apple M5 Pro, macOS 26.5, Go 1.26.1, wazero v1.12.0, against
`anydoc.wasm` 6,542,355 bytes (anydoc 0.1.7). The 5 MB figure is a 25,000-row
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
go build -tags anydoc_nowasm    # 6.59 MB smaller
```

`embeddedWASM` is then nil and `New` requires `WithWASM(r)` or
`WithWASMBytes(b)`. Useful for container layering, serverless size limits,
pinning a different anydoc build, or environments that forbid opaque embedded
blobs.

The saving is the 6.54 MB module plus ~47 KB of `embed` machinery. For scale,
`examples/convert` is 14.1 MB built normally and 7.6 MB with the tag.

The build tag affects the compiled binary, not `go get`: the module is in the
Go module either way.

## Tuning

```go
anydoc.New(
    anydoc.WithConcurrency(4),        // simultaneous guests; the main memory lever
    anydoc.WithMemoryLimitPages(1024), // 64 MiB per guest
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

How much a guest actually needs tracks the *uncompressed* content, not the
size of the file on disk — a zip bomb is small on disk and large in memory,
which is why the limit exists:

| document | pages needed |
|---|---|
| module minimum, converts nothing | 64 (4 MiB) |
| docx, 0.4 MB body | 192 (12 MiB) |
| docx, 2 MB body | 576 (36 MiB) |
| PDF, 7.5 MB file | 512–1024 (32–64 MiB) |
| docx, 5 MB body | 1280 (80 MiB) |

The default of 1024 pages converts an ordinary 7.5 MB PDF. Raise it if
conversions fail on inputs that convert fine elsewhere — the error says so
explicitly when the guest ran out of memory, and names this option. Each
concurrent guest can claim up to this much, so `WithConcurrency` multiplied by
this value is your real ceiling.

## Development

Tasks are run with [Task](https://taskfile.dev); `task --list` shows them all.

```
task test      # builds the wasip1 test stub, runs the harness suite
task verify    # everything CI runs, and the checklist before tagging
task wasm      # rebuilds the embedded module; needs Rust 1.88 + wasm-opt
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
built from the pinned crate by `task wasm`, run by hand rather than by CI, and
`anydoc.wasm.sha256` records the checksum of the committed copy. See
`anydoc.wasm.README` for the toolchain versions needed to reproduce it.

## Versioning

This module's version is independent of the crate's. The crate is pinned with
`=` in `rust/Cargo.toml`, and moving to a new upstream release is a deliberate
act: edit the pin, run `task wasm`, review what changed in the conversion
output, then tag. An upstream release on its own changes nothing here.

`anydoc.EmbeddedAnydocVersion` reports which crate version the embedded module
was built from, and `anydoc.Info()` prints it with the payload size. It is
copied from `rust/Cargo.lock` by `task sync-version`, and `task check-version`
fails the build if the two ever disagree — so it cannot quietly misreport what
is actually embedded.

`rust/Cargo.lock` is committed too. The `=` pin only fixes anydoc itself; the
lockfile is what stops a transitive dependency from silently changing the
conversion output between one rebuild and the next.

## License

MIT for this package — see `LICENSE`. The embedded module is built from
[anydoc](https://github.com/firecrawl/anydoc), also MIT — see `LICENSE-anydoc`.
