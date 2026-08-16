// Package anydoc converts office documents to Markdown by running the anydoc
// Rust library as WebAssembly, with no cgo and no external processes.
//
// The wasm module is embedded, so a binary built with this package is
// self-contained and cross-compiles anywhere Go does:
//
//	CGO_ENABLED=0 GOOS=windows GOARCH=arm64 go build ./...
//
// Basic use:
//
//	c, err := anydoc.New()
//	defer c.Close(ctx)
//	md, err := c.Convert(ctx, docBytes, "docx")
package anydoc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/samyfodil/wazy"
	"github.com/samyfodil/wazy/imports/wasi_snapshot_preview1"
	"github.com/samyfodil/wazy/sys"
)

// Converter runs conversions. It is safe for concurrent use and should be
// created once and reused: New compiles the module, which is the expensive
// part, and Convert then instantiates a cheap short-lived guest per document.
type Converter struct {
	runtime  wazy.Runtime
	compiled wazy.CompiledModule
	cache    wazy.CompilationCache // nil unless WithCompilationCache was given
	gate     chan struct{}
	maxBytes int
	pages    uint32 // kept so a guest OOM can name the limit it hit

	closeOnce sync.Once
	closeErr  error
}

// New compiles the wasm module and prepares the runtime.
//
// Compilation happens once, here, and costs a few hundred milliseconds. Do it
// at startup, not per request.
func New(opts ...Option) (*Converter, error) {
	cfg := defaultConfig()
	for _, o := range opts {
		o(&cfg)
	}

	module := cfg.wasm
	if module == nil {
		if !wasmEmbedded {
			return nil, ErrNoWASM
		}
		module = embeddedWASM
	}
	if len(module) == 0 {
		return nil, ErrNoWASM
	}

	ctx := context.Background()

	// Interpreter, not the optimising compiler. Three reasons, in order of
	// importance for a library that ships inside other people's binaries:
	//
	//  1. The compiler backend only covers amd64/arm64. The interpreter behaves
	//     identically on riscv64, ppc64le, 386 and anything else Go targets.
	//  2. The compiler needs mmap'd executable pages, which macOS hardened
	//     runtime and some seccomp profiles refuse. The interpreter never asks.
	//  3. Compiling this module costs ~2.7s and 638 MB of RSS, against ~100ms
	//     and 182 MB to interpret it, and 638 MB is an OOM kill in a 512 MB
	//     container. A library cannot assume it may write a compilation cache
	//     somewhere on the user's machine, so it cannot make that the default
	//     -- but an application knows where its own data lives, which is what
	//     WithCompilationCache is for: with a warm cache those figures become
	//     34ms and 50 MB, and the reasoning above stops applying.
	//
	// The cost is throughput, and it scales with document size rather than
	// being a flat overhead: measured on the real module, a 1 KB docx takes
	// 3.5ms interpreted against 0.4ms compiled, but a docx with a 5 MB
	// uncompressed body takes 11s against 0.9s. Small documents are free;
	// multi-megabyte ones are not. Callers who need the throughput and can
	// afford the memory opt in with WithCompiler; everyone else bounds the
	// tail with WithMaxInputBytes and a context deadline.
	var rtCfg wazy.RuntimeConfig
	if cfg.compiler {
		// NewRuntimeConfig, not NewRuntimeConfigCompiler: the latter panics
		// where the backend is unavailable, and this package is meant to run
		// on platforms that have none. Auto picks the compiler when the host
		// supports it and degrades to the interpreter when it does not.
		rtCfg = wazy.NewRuntimeConfig()
	} else {
		rtCfg = wazy.NewRuntimeConfigInterpreter()
	}
	rtCfg = rtCfg.
		WithMemoryLimitPages(cfg.memoryPages).
		WithCloseOnContextDone(true)

	// The compiler's output is worth keeping. Compiling this module is what
	// costs seconds and hundreds of megabytes; loading the result back is
	// neither. Only the compiler produces anything to store, so an interpreted
	// runtime configured with a cache simply never writes to it.
	var cache wazy.CompilationCache
	if cfg.cacheDir != "" {
		var err error
		if cache, err = wazy.NewCompilationCacheWithDir(cfg.cacheDir); err != nil {
			return nil, fmt.Errorf("anydoc: compilation cache %q: %w", cfg.cacheDir, err)
		}
		rtCfg = rtCfg.WithCompilationCache(cache)
	}

	rt := wazy.NewRuntimeWithConfig(ctx, rtCfg)

	// The cache owns an engine of its own and outlives the runtime, so every
	// exit from here has to release both.
	fail := func(err error) (*Converter, error) {
		rt.Close(ctx)
		if cache != nil {
			cache.Close(ctx)
		}
		return nil, err
	}

	// WASI is needed for stdio and for random_get, which std's HashMap seeding
	// reaches for. Nothing else is granted: no filesystem, no clock, no sockets.
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, rt); err != nil {
		return fail(fmt.Errorf("anydoc: instantiate wasi: %w", err))
	}

	compiled, err := rt.CompileModule(ctx, module)
	if err != nil {
		return fail(fmt.Errorf("anydoc: compile module: %w", err))
	}

	return &Converter{
		runtime:  rt,
		compiled: compiled,
		cache:    cache,
		gate:     make(chan struct{}, cfg.concurrency),
		maxBytes: cfg.maxInputBytes,
		pages:    cfg.memoryPages,
	}, nil
}

// Convert turns a document into Markdown.
//
// hint is a bare extension such as "docx" that selects the parser. Pass "" to
// detect from content, which works for every format except CSV, which has no
// signature and must be named.
//
// Each call gets a fresh guest instance with its own linear memory. A large
// document therefore cannot leave memory permanently claimed, and a malformed
// one cannot leak state into the next call.
func (c *Converter) Convert(ctx context.Context, doc []byte, hint string) (string, error) {
	if len(doc) == 0 {
		return "", &ConvertError{Kind: ErrUnsupported, Detail: "empty input"}
	}
	if c.maxBytes > 0 && len(doc) > c.maxBytes {
		return "", &ConvertError{
			Kind:   ErrResourceLimit,
			Detail: fmt.Sprintf("input is %d bytes, limit is %d", len(doc), c.maxBytes),
		}
	}

	// Bound concurrent guests. Each holds its own linear memory, so without
	// this the peak footprint is set by the caller's goroutine count.
	select {
	case c.gate <- struct{}{}:
		defer func() { <-c.gate }()
	case <-ctx.Done():
		return "", ctx.Err()
	}

	var stdout, stderr bytes.Buffer

	modCfg := wazy.NewModuleConfig().
		// Anonymous. A wasip1 command module carries a name, and wazy refuses
		// to instantiate the same name twice; without this, the second
		// concurrent Convert fails with "module already instantiated".
		WithName("").
		WithArgs("anydoc", hint).
		WithStdin(bytes.NewReader(doc)).
		WithStdout(&stdout).
		WithStderr(&stderr)

	mod, err := c.runtime.InstantiateModule(ctx, c.compiled, modCfg)
	if mod != nil {
		// A command module has already run _start by the time this returns.
		defer mod.Close(ctx)
	}

	if err != nil {
		guestErr := strings.TrimSpace(stderr.String())

		// wazy reports a command module's exit through sys.ExitError,
		// including a clean exit(0) in some paths, so check the code before
		// treating this as a failure.
		var exitErr *sys.ExitError
		if errors.As(err, &exitErr) {
			if code := exitErr.ExitCode(); code != 0 {
				// Running out of guest memory can surface either as a clean
				// exit from the shim or as a trap below. Both mean the same
				// thing to a caller, so answer them the same way.
				if guestOutOfMemory(guestErr) {
					return "", errGuestOutOfMemory(c.pages, guestErr)
				}
				return "", errorForExit(int(code), guestErr)
			}
			return stdout.String(), nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		if guestOutOfMemory(guestErr) {
			return "", errGuestOutOfMemory(c.pages, guestErr)
		}
		return "", &ConvertError{
			Kind:   ErrWASM,
			Detail: fmt.Sprintf("%v; guest stderr: %s", err, guestErr),
		}
	}

	return stdout.String(), nil
}

// ConvertReader is a convenience wrapper that reads r fully before converting.
// The whole document has to be in memory regardless: it is copied into the
// guest's linear memory.
func (c *Converter) ConvertReader(ctx context.Context, r io.Reader, hint string) (string, error) {
	var buf bytes.Buffer
	if c.maxBytes > 0 {
		// Read one byte past the limit so an oversized input is rejected rather
		// than silently truncated.
		if _, err := io.Copy(&buf, io.LimitReader(r, int64(c.maxBytes)+1)); err != nil {
			return "", fmt.Errorf("anydoc: read input: %w", err)
		}
	} else if _, err := io.Copy(&buf, r); err != nil {
		return "", fmt.Errorf("anydoc: read input: %w", err)
	}
	return c.Convert(ctx, buf.Bytes(), hint)
}

// Close releases the runtime, and the compilation cache when there is one. The
// Converter is unusable afterwards. Cached entries stay on disk: that is the
// point of them.
func (c *Converter) Close(ctx context.Context) error {
	c.closeOnce.Do(func() {
		c.closeErr = c.runtime.Close(ctx)
		if c.cache != nil {
			// A cache holds its own engine, which a runtime Close does not
			// reach. Report its failure only if the runtime closed cleanly,
			// so the first error survives.
			if err := c.cache.Close(ctx); err != nil && c.closeErr == nil {
				c.closeErr = err
			}
		}
	})
	return c.closeErr
}
