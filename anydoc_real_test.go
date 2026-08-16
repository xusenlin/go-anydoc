//go:build !anydoc_nowasm

// Conversion tests that run against the real embedded module.
//
// The build tag is the gate: embed.go only compiles when anydoc.wasm is in the
// tree, so this file is present exactly when there is something real to test.
// The stub suite in anydoc_test.go covers the harness and runs everywhere;
// this one covers what the harness is for.

package anydoc

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

func realConverter(t *testing.T) *Converter {
	t.Helper()
	c, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { c.Close(context.Background()) })
	return c
}

// CSV is the one format with no signature, so it exercises the hint path that
// content detection can never reach.
func TestRealCSV(t *testing.T) {
	c := realConverter(t)

	// With a BOM, as Excel on Windows writes it. Chinese throughout: this
	// class of library tends to break on multi-byte content and column widths.
	in := []byte("\xef\xbb\xbf姓名,部门,入职日期\n张三,工程,2024-03-01\n李四,设计,2023-11-15\n")

	md, err := c.Convert(context.Background(), in, "csv")
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	for _, want := range []string{"姓名", "部门", "入职日期", "张三", "工程", "李四", "设计"} {
		if !strings.Contains(md, want) {
			t.Errorf("missing %q in output:\n%s", want, md)
		}
	}
	// A header separator row is what makes it a Markdown table rather than
	// lines of text.
	if !strings.Contains(md, "---") {
		t.Errorf("not rendered as a table:\n%s", md)
	}
	// The BOM must be consumed, not carried into the first cell.
	if strings.Contains(md, "\ufeff") {
		t.Error("BOM leaked into the Markdown")
	}
}

// minimalDocx builds a WordprocessingML package that is valid enough to parse:
// content types, a package relationship, and a document with one paragraph and
// one two-column table.
func minimalDocx(t testing.TB, para string, cells [][2]string) []byte {
	t.Helper()

	var doc strings.Builder
	doc.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
		`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>`)
	doc.WriteString(`<w:p><w:r><w:t>` + para + `</w:t></w:r></w:p>`)
	doc.WriteString(`<w:tbl>`)
	for _, row := range cells {
		doc.WriteString(`<w:tr>`)
		for _, cell := range row {
			doc.WriteString(`<w:tc><w:p><w:r><w:t>` + cell + `</w:t></w:r></w:p></w:tc>`)
		}
		doc.WriteString(`</w:tr>`)
	}
	doc.WriteString(`</w:tbl></w:body></w:document>`)

	files := map[string]string{
		"[Content_Types].xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
			`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
			`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
			`<Default Extension="xml" ContentType="application/xml"/>` +
			`<Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>` +
			`</Types>`,
		"_rels/.rels": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
			`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
			`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>` +
			`</Relationships>`,
		"word/document.xml": doc.String(),
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	// [Content_Types].xml first, as the OPC specification requires.
	for _, name := range []string{"[Content_Types].xml", "_rels/.rels", "word/document.xml"} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := w.Write([]byte(files[name])); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

func TestRealDOCX(t *testing.T) {
	c := realConverter(t)
	in := minimalDocx(t, "季度报告", [][2]string{
		{"指标", "数值"},
		{"营业收入", "1,234 万元"},
	})

	// Detection from content, with no hint: the whole point of the signature
	// path. A docx is a zip, so this also proves the container is inspected
	// rather than sniffed on the first four bytes.
	md, err := c.Convert(context.Background(), in, "")
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	for _, want := range []string{"季度报告", "指标", "数值", "营业收入", "1,234 万元"} {
		if !strings.Contains(md, want) {
			t.Errorf("missing %q in output:\n%s", want, md)
		}
	}

	// Naming the format explicitly has to agree with detecting it.
	hinted, err := c.Convert(context.Background(), in, "docx")
	if err != nil {
		t.Fatalf("Convert with hint: %v", err)
	}
	if hinted != md {
		t.Errorf("hinted and detected output differ:\n--- hinted ---\n%s\n--- detected ---\n%s", hinted, md)
	}
}

// The exit-code table is a contract between rust/src/main.rs and errors.go.
// The stub suite proves the Go side maps codes correctly; this proves the real
// module emits the codes the table expects.
func TestRealErrorMapping(t *testing.T) {
	c := realConverter(t)
	ctx := context.Background()

	t.Run("unrecognised content", func(t *testing.T) {
		_, err := c.Convert(ctx, []byte("just some plain text, no signature"), "")
		if !errors.Is(err, ErrUnsupported) {
			t.Fatalf("got %v, want ErrUnsupported", err)
		}
	})

	t.Run("unknown hint", func(t *testing.T) {
		// EXIT_BAD_HINT, a code the stub cannot produce: it is raised before
		// the crate is ever called.
		_, err := c.Convert(ctx, []byte("anything"), "nosuchformat")
		if !errors.Is(err, ErrUnsupported) {
			t.Fatalf("got %v, want ErrUnsupported", err)
		}
		var ce *ConvertError
		if errors.As(err, &ce) && ce.Code != exitBadHint {
			t.Errorf("got exit code %d, want %d", ce.Code, exitBadHint)
		}
	})

	t.Run("docx with no document part", func(t *testing.T) {
		// A structurally valid zip that is not a Word package.
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		w, _ := zw.Create("hello.txt")
		w.Write([]byte("not a document"))
		zw.Close()

		_, err := c.Convert(ctx, buf.Bytes(), "docx")
		if err == nil {
			t.Fatal("expected an error")
		}
		// Which of the two the crate picks is its business; both are document
		// problems rather than harness failures, and neither may be ErrWASM.
		if !errors.Is(err, ErrMissingPart) && !errors.Is(err, ErrMalformed) {
			t.Fatalf("got %v, want ErrMissingPart or ErrMalformed", err)
		}
	})

	t.Run("truncated zip", func(t *testing.T) {
		in := minimalDocx(t, "x", [][2]string{{"a", "b"}})
		_, err := c.Convert(ctx, in[:len(in)/2], "docx")
		if err == nil {
			t.Fatal("expected an error")
		}
		if errors.Is(err, ErrWASM) {
			t.Errorf("corrupt input surfaced as an internal error: %v", err)
		}
	})
}

// WithCompiler must not change what comes out, only how fast. Where the
// backend is unavailable wazy falls back to the interpreter, so this passes
// on every platform either way -- which is the point of routing through
// NewRuntimeConfig rather than NewRuntimeConfigCompiler, the latter panicking
// on platforms with no backend.
func TestRealWithCompiler(t *testing.T) {
	ctx := context.Background()
	in := minimalDocx(t, "季度报告", [][2]string{
		{"指标", "数值"},
		{"营业收入", "1,234 万元"},
	})

	interp := realConverter(t)
	want, err := interp.Convert(ctx, in, "docx")
	if err != nil {
		t.Fatalf("interpreter: %v", err)
	}

	c, err := New(WithCompiler())
	if err != nil {
		t.Fatalf("New(WithCompiler): %v", err)
	}
	defer c.Close(ctx)

	got, err := c.Convert(ctx, in, "docx")
	if err != nil {
		t.Fatalf("compiler: %v", err)
	}
	if got != want {
		t.Errorf("engines disagree:\n--- compiler ---\n%s\n--- interpreter ---\n%s", got, want)
	}

	// One compile, many conversions: the second call must not recompile.
	if _, err := c.Convert(ctx, in, "docx"); err != nil {
		t.Fatalf("second convert: %v", err)
	}
}

// The cache is what makes WithCompiler affordable outside a long-lived server,
// so what has to hold is that a second process reusing the directory produces
// the same Markdown -- speed is the reward, correctness is the requirement.
// The interpreter half of this matters just as much: it emits no machine code,
// and silently filling a directory with nothing would be a lie in the docs.
func TestRealCompilationCache(t *testing.T) {
	ctx := context.Background()
	in := minimalDocx(t, "季度报告", [][2]string{{"指标", "数值"}})

	dir := t.TempDir()
	convert := func() string {
		t.Helper()
		c, err := New(WithCompiler(), WithCompilationCache(dir))
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		defer c.Close(ctx)
		md, err := c.Convert(ctx, in, "docx")
		if err != nil {
			t.Fatalf("Convert: %v", err)
		}
		return md
	}

	cold := convert()
	entries := countFiles(t, dir)
	if entries == 0 {
		// wazy degrades to the interpreter where the compiler backend is
		// unavailable, and then there is nothing to write. Not a failure --
		// but the rest of this test would be asserting nothing.
		t.Skip("no cache entries written; the compiler backend is unavailable here")
	}

	// A warm run is a different Converter reading back what the cold one left.
	if warm := convert(); warm != cold {
		t.Errorf("warm cache changed the output:\n--- cold ---\n%s\n--- warm ---\n%s", cold, warm)
	}
	if after := countFiles(t, dir); after != entries {
		t.Errorf("warm run rewrote the cache: %d entries before, %d after", entries, after)
	}

	// The interpreter has no machine code to persist, so its directory stays
	// empty. WithCompilationCache documents this; keep it true.
	interpDir := t.TempDir()
	c, err := New(WithCompilationCache(interpDir))
	if err != nil {
		t.Fatalf("New(interpreter): %v", err)
	}
	defer c.Close(ctx)
	if _, err := c.Convert(ctx, in, "docx"); err != nil {
		t.Fatalf("Convert(interpreter): %v", err)
	}
	if n := countFiles(t, interpDir); n != 0 {
		t.Errorf("interpreter wrote %d cache entries, want 0", n)
	}
}

// countFiles reports how many regular files exist under dir, at any depth:
// wazy nests entries in a version- and platform-specific subdirectory.
func countFiles(t *testing.T, dir string) int {
	t.Helper()
	n := 0
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			n++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return n
}

// A guest that exhausts its linear memory traps as a bare "unreachable" with
// a wasm stack trace attached. On its own that tells a caller nothing about
// the single setting that would fix it, so the error has to name the limit
// and the option.
func TestRealGuestOutOfMemory(t *testing.T) {
	// A 2 MB body needs about 576 pages; 192 is comfortably under that while
	// still clearing the module's own 64-page floor, so New succeeds and the
	// failure happens where this test wants it, during conversion.
	c, err := New(WithMemoryLimitPages(192))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close(context.Background())

	cells := make([][2]string, 10000)
	for i := range cells {
		cells[i] = [2]string{"某个中文单元格内容用来撑大文档体积", "另一个中文单元格内容用来撑大文档体积"}
	}
	in := minimalDocx(t, "大文档", cells)

	_, err = c.Convert(context.Background(), in, "docx")
	if err == nil {
		t.Fatal("expected the guest to run out of memory")
	}
	if !errors.Is(err, ErrWASM) {
		t.Fatalf("got %v, want ErrWASM", err)
	}
	msg := err.Error()
	for _, want := range []string{"ran out of memory", "192 page", "WithMemoryLimitPages"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error does not mention %q: %s", want, msg)
		}
	}
	// The wasm stack trace is noise once the cause is known.
	if strings.Contains(msg, "wasm stack trace") {
		t.Errorf("stack trace not suppressed: %s", msg)
	}
}

// Info is what host applications print for provenance, so the embedded build
// has to report a version and a non-trivial payload.
func TestRealInfo(t *testing.T) {
	if EmbeddedAnydocVersion == "" {
		t.Error("EmbeddedAnydocVersion is empty in an embedded build")
	}
	if n := len(embeddedWASM); n < 1<<20 {
		t.Errorf("embedded module is %d bytes, implausibly small for a real one", n)
	}
	if !strings.Contains(Info(), EmbeddedAnydocVersion) {
		t.Errorf("Info() does not report the version: %q", Info())
	}
}
