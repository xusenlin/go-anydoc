//go:build !anydoc_nowasm

// Conversion benchmarks against the real embedded module.
//
// These exist so the numbers in the README can be reproduced rather than taken
// on faith, and re-measured whenever the crate or the runtime moves. They were
// hand-run before, which meant the figures could not be checked by anyone else
// and quietly aged past the build they described.
//
// Inputs are generated here rather than committed, so the corpus is part of the
// source and a checkout is enough to reproduce a run:
//
//	go test -run '^$' -bench . -benchtime 3x
//
// PDFs are the exception: no PDF small enough to commit is heavy enough to be
// worth measuring, and the ones that are heavy enough are somebody's document.
// BenchmarkConvertPDF therefore runs against a file named by the environment
// and skips when there is none:
//
//	ANYDOC_BENCH_PDF=/path/to/big.pdf go test -run '^$' -bench PDF -benchtime 3x
//
// Each iteration is one Convert on an already-compiled module. Compilation is
// deliberately outside the timer: it happens once per process in real use, and
// including it would bury the per-document cost this measures. The interpreted
// large-document cases run for seconds apiece, so -benchtime is a small count
// rather than a duration.
package anydoc

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// benchDocs are the shapes worth distinguishing: one where instantiation
// dominates, and one where conversion does. The large body is uncompressed
// size -- it zips down to tens of kilobytes, and the size that costs time is
// the one the guest walks.
//
// The large case needs its own memory limit. A 5 MB body does not fit the
// 64 MiB default: the guest holds the parsed document and the rendered
// Markdown at once, so peak use is a multiple of the input. That is a property
// of the document, not of the runtime -- both engines need the same headroom.
var benchDocs = []struct {
	name  string
	rows  int
	pages uint32 // 0 leaves the default
}{
	{"small", 1, 0},        // ~1 KB
	{"large", 25000, 4096}, // ~5 MB body, 256 MiB
}

func benchInput(tb testing.TB, rows int) []byte {
	tb.Helper()
	cells := make([][2]string, rows)
	for i := range cells {
		cells[i] = [2]string{"某个中文单元格内容用来撑大文档体积", "另一个中文单元格内容用来撑大文档体积"}
	}
	return minimalDocx(tb, "基准文档", cells)
}

func BenchmarkConvertDOCX(b *testing.B) {
	for _, engine := range []struct {
		name string
		opts []Option
	}{
		{"compiled", []Option{WithCompiler()}},
		{"interpreted", nil},
	} {
		for _, doc := range benchDocs {
			b.Run(fmt.Sprintf("%s/%s", engine.name, doc.name), func(b *testing.B) {
				in := benchInput(b, doc.rows)

				// A fresh slice, not append onto engine.opts: that would
				// write through to the shared backing array once the engine
				// list carries more than one option.
				opts := make([]Option, 0, len(engine.opts)+1)
				opts = append(opts, engine.opts...)
				if doc.pages != 0 {
					opts = append(opts, WithMemoryLimitPages(doc.pages))
				}

				// Outside the timer on purpose: New compiles the module.
				c, err := New(opts...)
				if err != nil {
					b.Fatalf("New: %v", err)
				}
				defer c.Close(context.Background())

				// One conversion before timing, so the first measured
				// iteration is not the one that pays for lazily initialised
				// state inside the runtime.
				if _, err := c.Convert(context.Background(), in, "docx"); err != nil {
					b.Fatalf("Convert: %v", err)
				}

				b.SetBytes(int64(len(in)))
				b.ReportAllocs()
				b.ResetTimer()
				for b.Loop() {
					if _, err := c.Convert(context.Background(), in, "docx"); err != nil {
						b.Fatalf("Convert: %v", err)
					}
				}
			})
		}
	}
}

// BenchmarkConvertPDF is the heavy end of the corpus: a real PDF, which is
// mostly compute and linear-memory traffic rather than the XML walking a docx
// is. Set ANYDOC_BENCH_PDF to a file to run it.
func BenchmarkConvertPDF(b *testing.B) {
	path := os.Getenv("ANYDOC_BENCH_PDF")
	if path == "" {
		b.Skip("set ANYDOC_BENCH_PDF to a PDF file to run this")
	}
	in, err := os.ReadFile(path)
	if err != nil {
		b.Fatalf("read %s: %v", path, err)
	}
	b.Logf("%s, %d bytes", filepath.Base(path), len(in))

	for _, engine := range []struct {
		name string
		opts []Option
	}{
		{"compiled", []Option{WithCompiler()}},
		{"interpreted", nil},
	} {
		b.Run(engine.name, func(b *testing.B) {
			opts := make([]Option, 0, len(engine.opts)+2)
			opts = append(opts, engine.opts...)
			// A multi-megabyte PDF needs both limits raised: the default
			// input cap rejects it before the guest sees it, and the default
			// memory limit is well under what parsing it costs.
			opts = append(opts, WithMemoryLimitPages(4096), WithMaxInputBytes(len(in)))

			c, err := New(opts...)
			if err != nil {
				b.Fatalf("New: %v", err)
			}
			defer c.Close(context.Background())

			if _, err := c.Convert(context.Background(), in, "pdf"); err != nil {
				b.Fatalf("Convert: %v", err)
			}

			b.SetBytes(int64(len(in)))
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if _, err := c.Convert(context.Background(), in, "pdf"); err != nil {
					b.Fatalf("Convert: %v", err)
				}
			}
		})
	}
}

// BenchmarkNew measures the cost Convert is deliberately not charged for:
// decode, validate, compile, instantiate. It is what WithCompilationCache
// exists to amortise, and the reason a Converter is meant to be created once
// and reused.
func BenchmarkNew(b *testing.B) {
	for _, engine := range []struct {
		name string
		opts []Option
	}{
		{"compiled", []Option{WithCompiler()}},
		{"interpreted", nil},
	} {
		b.Run(engine.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				c, err := New(engine.opts...)
				if err != nil {
					b.Fatalf("New: %v", err)
				}
				c.Close(context.Background())
			}
		})
	}
}
