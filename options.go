package anydoc

import (
	"fmt"
	"io"
)

type config struct {
	wasm          []byte
	concurrency   int
	memoryPages   uint32
	maxInputBytes int
	compiler      bool
	cacheDir      string
}

func defaultConfig() config {
	return config{
		// Conservative on purpose: this package ends up inside self-hosted
		// binaries running on whatever machine the user has.
		concurrency:   4,
		memoryPages:   1024,             // 1024 * 64 KiB = 64 MiB per guest
		maxInputBytes: 64 * 1024 * 1024, // 64 MiB
	}
}

// Option configures a Converter.
type Option func(*config)

// WithWASM supplies the module instead of using the embedded one.
//
// This is the counterpart to building with -tags anydoc_nowasm, where nothing
// is embedded and this option becomes mandatory. It is also useful without the
// tag: pinning a different anydoc build, loading from a shared volume, or
// keeping the payload in its own container layer.
//
// The module is read immediately; r is not retained.
func WithWASM(r io.Reader) Option {
	return func(c *config) {
		b, err := io.ReadAll(r)
		if err == nil {
			c.wasm = b
		}
		// A read failure leaves c.wasm nil, which New reports as ErrNoWASM.
	}
}

// WithWASMBytes is WithWASM for a module already in memory.
func WithWASMBytes(b []byte) Option {
	return func(c *config) { c.wasm = b }
}

// WithConcurrency caps simultaneous guest instances. Each holds its own linear
// memory, so this is the main lever on peak memory. Default 4.
func WithConcurrency(n int) Option {
	return func(c *config) {
		if n > 0 {
			c.concurrency = n
		}
	}
}

// WithMemoryLimitPages caps one guest's linear memory in 64 KiB pages.
// Default 1024 (64 MiB).
//
// What a guest needs tracks the uncompressed content rather than the input's
// size on disk, so a compact file can still be expensive. Measured against the
// real module: 64 pages is the module's own floor and converts nothing, a
// docx with a 0.4 MB body needs 192, a 2 MB body 576, a 5 MB body 1280, and a
// 7.5 MB PDF needs between 512 and 1024.
//
// Raise this when conversions fail with ErrWASM on inputs that convert fine
// elsewhere; the error says so explicitly when the guest ran out of memory.
// Each concurrent guest can claim up to this much, so this multiplied by
// WithConcurrency is the real ceiling.
//
// Validated at New time against the module's declared minimum, so too low a
// value fails immediately rather than at conversion.
func WithMemoryLimitPages(pages uint32) Option {
	return func(c *config) {
		if pages > 0 {
			c.memoryPages = pages
		}
	}
}

// WithMaxInputBytes rejects documents larger than n before any wasm runs.
// Zero disables the check. Default 64 MiB.
func WithMaxInputBytes(n int) Option {
	return func(c *config) { c.maxInputBytes = n }
}

// WithCompiler trades startup cost and memory for throughput, by having wazero
// compile the module to native code instead of interpreting it.
//
// Measured on the real module, per document: a 1 KB docx goes from 3.5ms to
// 0.4ms, one with a 5 MB uncompressed body from 11.1s to 0.86s, and a 7.5 MB
// PDF from 41s to 3.4s. The price is paid once in New, which goes from ~100ms
// and 182 MB of RSS to ~2.7s and 638 MB. That 638 MB is an OOM kill in a
// 512 MB container, which is why this is opt-in rather than the default.
//
// The compilation is paid once, in New, not per document: Convert only
// instantiates the already-compiled module. So this pays off in a long-lived
// process that reuses one Converter, and is a pure loss in a short-lived one
// that converts a single small document and exits -- there it buys ~3ms for
// ~2.7s. Leave it off for tight containers too, unless you can also give it
// WithCompilationCache, which removes both of those objections after the
// first run.
//
// This is a request, not a guarantee. The compiler backend needs amd64 or
// arm64 (with SSE4.1 on amd64) on a mainstream OS, and needs the host to allow
// mmap'd executable pages -- macOS hardened runtime and some seccomp profiles
// refuse them. Where it is unavailable, wazero falls back to the interpreter
// silently: the conversion still happens, at interpreter speed. Nothing here
// affects cross-compilation, since wazero is pure Go.
func WithCompiler() Option {
	return func(c *config) { c.compiler = true }
}

// WithCompilationCache persists the compiler's output under dirname, so the
// cost of WithCompiler is paid once per machine instead of once per process.
//
// It changes what WithCompiler costs more than it changes what it does.
// Measured on the real module, a hit turns New from 2.8s and 647 MB of peak
// RSS into 34ms and 50 MB, because the machine code is read back rather than
// produced -- the memory WithCompiler is expensive for is the compiler
// working, not the compiled module sitting there. The directory holds ~23 MB.
//
// This only affects WithCompiler. The interpreter emits no machine code, so
// there is nothing to persist and this option does nothing.
//
// An entry is keyed by the module, the CPU's feature bits, the runtime version
// and the target platform. Anything else is a miss, and a miss simply compiles
// again and writes a new entry -- it will not hand the host machine code it
// cannot run. That also makes the directory disposable: deleting it costs one
// recompilation and nothing else, and what it holds is machine code, never
// anything a caller passed to Convert.
//
// Give it a directory the process owns and that survives restarts; a temporary
// directory wastes the point. It is created if missing, and Close releases it.
func WithCompilationCache(dirname string) Option {
	return func(c *config) { c.cacheDir = dirname }
}

// Info reports what this build is carrying, for diagnostics and version
// reporting in host applications.
func Info() string {
	if !wasmEmbedded {
		return "go-anydoc (no embedded wasm; built with -tags anydoc_nowasm)"
	}
	return fmt.Sprintf("go-anydoc (embedded anydoc %s, %d bytes)",
		EmbeddedAnydocVersion, len(embeddedWASM))
}
