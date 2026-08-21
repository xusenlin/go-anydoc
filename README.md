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

Requires Go 1.25 or newer, which is what wazy requires.

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

**Interpreter, not the optimising compiler.** wazy can either translate the
module to native machine code up front (fast to run, expensive to load, amd64
and arm64 only) or interpret it instruction by instruction (cheap to load,
portable everywhere, slower to run). This package interprets by default,
because a library that ships inside other people's binaries cannot assume it
may write a compilation cache somewhere, cannot assume the host allows the
executable pages the compiler needs — macOS hardened runtime and some seccomp
profiles refuse them — and cannot assume the target is amd64 or arm64.
`WithCompiler` opts into the other side of that trade.

An *application* does know where its own data lives, and that changes the
arithmetic: `WithCompilationCache` makes the compiler's cost a one-time
2.5 s and 630 MB instead of a per-start one, and every start after that is
7 ms at 36 MB — cheaper than interpreting, and two orders of magnitude
faster to convert with.
The defaults below assume no cache, because a library cannot assume one.

The trade is real and it scales with document size, so measure against your own
corpus before assuming it is free:

| | interpreter (default) | compiler | compiler + warm cache |
|---|---|---|---|
| `New()` — once per process | 101 ms | 2.5 s | **7 ms** |
| 1 KB docx | 2.4 ms | 0.51 ms | 0.51 ms |
| docx with a 5 MB uncompressed body | 8.4 s | 0.18 s | 0.18 s |
| 7.5 MB PDF | 34.5 s | 0.77 s | 0.77 s |
| RSS after `New()` | 120 MB | 630 MB | **36 MB** |

The third column is the second one after `WithCompilationCache` has a directory
to read from — same execution, none of the startup. Conversion figures are
identical because the cache changes how the machine code is obtained, not what
it is. Only the first run on a machine pays the second column.

Ordinary office documents are in the second row's territory and cost nothing
worth optimising. Multi-megabyte ones are ~45× slower than they would be
compiled, so if you convert those, either bound the tail with
`WithMaxInputBytes` and a context deadline — cancellation interrupts the guest
mid-conversion — or opt into `WithCompiler`.

**wazy, not wazero.** [wazy](https://github.com/samyfodil/wazy) is a pure-Go
runtime descended from wazero that spends its effort on the memory-access paths
this workload lives in. Same module, same inputs, same machine:

| converting | wazero v1.12.0 | wazy v0.1.3 | |
|---|---|---|---|
| 1 KB docx, compiled | 0.73 ms | 0.51 ms | 1.4× |
| docx with a 5 MB body, compiled | 0.85 s | 0.18 s | **4.7×** |
| 7.5 MB PDF, compiled | 3.63 s | 0.77 s | **4.7×** |
| 1 KB docx, interpreted | 2.55 ms | 2.39 ms | 1.1× |
| docx with a 5 MB body, interpreted | 10.9 s | 8.4 s | 1.3× |
| 7.5 MB PDF, interpreted | 43.8 s | 34.5 s | 1.3× |

Long compute is where it wins, and the longer the compute the wider the gap.
Allocation is the other half of it: interpreting that PDF costs 472 allocations
on wazy against 58 million on wazero, and 100 MB against 1.5 GB.

The port was an import change. Same API, same embedded module, same exit-code
ABI, byte-identical output, and it still cross-compiles to riscv64, ppc64le,
386 and s390x.

The trade, stated plainly: wazy is a month old, has one author, and makes no
API-stability promise; wazero is mature, widely deployed, and has a company
behind it. This package took the newer one because converting documents that
take long enough for 4.7× to matter is the whole job — and because the way back
is the same one line.

<sub>Every figure on this page comes from `bench_test.go`, so it can be checked
rather than believed: `go test -run '^$' -bench . -benchtime 3x`, and
`ANYDOC_BENCH_PDF=big.pdf` for the PDF rows. Measured on Apple M5 Pro (18-core),
48 GB, macOS 26.5, Go 1.26.1, `CGO_ENABLED=0`, against `anydoc.wasm` 6,781,177
bytes (anydoc 0.2.3). The 5 MB figure is a 25,000-row table — 42 KB zipped,
since the size that costs time is the uncompressed body — and needs
`WithMemoryLimitPages(1280)`, above the default.</sub>

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
go build -tags anydoc_nowasm    # 6.84 MB smaller
```

`embeddedWASM` is then nil and `New` requires `WithWASM(r)` or
`WithWASMBytes(b)`. Useful for container layering, serverless size limits,
pinning a different anydoc build, or environments that forbid opaque embedded
blobs.

The saving is the 6.78 MB module plus ~56 KB of `embed` machinery. For scale,
`examples/convert` is 14.5 MB built normally and 7.7 MB with the tag.

The build tag affects the compiled binary, not `go get`: the module is in the
Go module either way.

## Tuning

```go
anydoc.New(
    anydoc.WithConcurrency(4),        // simultaneous guests; the main memory lever
    anydoc.WithMemoryLimitPages(1024), // 64 MiB per guest
    anydoc.WithMaxInputBytes(64<<20),
    anydoc.WithCompiler(),            // throughput over startup cost; see below
    anydoc.WithCompilationCache(dir), // and pay that cost only once; see below
)
```

### `WithCompiler`

Compiles the module to native code instead of interpreting it, turning the
table above from the left column into the right one. The cost is paid **once,
in `New`** — `Convert` only instantiates the already-compiled module — so it
pays off in a long-lived process that reuses one `Converter`, and is a pure
loss in a short-lived one that converts a single small document and exits
(~2.4 s bought to save ~1.9 ms).

It is a request, not a guarantee. The backend needs amd64 or arm64 on a
mainstream OS, plus a host that permits mmap'd executable pages. Where that
does not hold — riscv64, ppc64le, 386, macOS hardened runtime, some seccomp
profiles — wazy falls back to the interpreter silently and the conversion
still happens, just at interpreter speed. Cross-compilation is unaffected
either way: wazy is pure Go, and this option changes no build constraints.

### `WithCompilationCache`

Persists the compiler's output under a directory you own, so `WithCompiler`
costs what it costs once per machine rather than once per process:

| `New()` with `WithCompiler` | time | peak RSS |
|---|---|---|
| cold — compiling | 2.5 s | 630 MB |
| warm — reading the result back | **7 ms** | **36 MB** |

That gap is the whole argument against `WithCompiler` disappearing. The memory
it is expensive for is the compiler *working*, not the compiled module sitting
there; a hit loads machine code instead of producing it. The directory holds
about 15 MB.

This only affects `WithCompiler`. The interpreter emits no machine code, so
there is nothing to persist and the option does nothing — the directory stays
empty, and the test suite asserts it.

An entry is keyed by the module, the CPU's feature bits, the wazy version and
the target platform. Anything else is a miss, and a miss just compiles again
and writes a new entry; it will never hand a host machine code it cannot run.
So the directory is disposable — deleting it costs one recompilation — and it
holds machine code, never anything a caller passed to `Convert`.

Two things follow for anyone shipping this. Give it somewhere that survives a
restart, or the point is lost. And prefer to let it fill at runtime rather than
baking it into an image: the CPU feature bits are in the key, and a CI builder
rarely shares them with wherever the image ends up.

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

`bench_test.go` produces every figure in this README:

```
go test -run '^$' -bench . -benchtime 3x
ANYDOC_BENCH_PDF=big.pdf go test -run '^$' -bench PDF -benchtime 3x
```

Docx inputs are generated in the benchmark, so a checkout is enough to
reproduce a run. PDFs are not: none small enough to commit is heavy enough to
be worth measuring, so that one takes a path from the environment and skips
without it. Re-run both after changing the crate pin or the runtime — the
figures here are only as good as the build they were taken on.

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

### Releasing

```
task verify                # everything that has to hold
git tag -a vX.Y.Z && git push origin vX.Y.Z
```

There is nothing else to keep in step. The runtime experiment used to live on
its own branch, held by a `replace` and a pre-release tag that every release
had to be careful not to overtake; wazy reaching a tagged release made the
branch unnecessary, and `main` now depends on it like any other module.

## License

MIT for this package — see `LICENSE`. The embedded module is built from
[anydoc](https://github.com/firecrawl/anydoc), also MIT — see `LICENSE-anydoc`.
