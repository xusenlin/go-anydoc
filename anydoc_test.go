package anydoc

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// The stub speaks the same ABI as the real module but does not convert
// anything; see internal/stub.
func newStub(t *testing.T, opts ...Option) *Converter {
	t.Helper()
	b, err := os.ReadFile("testdata/stub.wasm")
	if err != nil {
		t.Skipf("stub.wasm not built: %v", err)
	}
	c, err := New(append([]Option{WithWASMBytes(b)}, opts...)...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { c.Close(context.Background()) })
	return c
}

func TestConvertRoundTrip(t *testing.T) {
	c := newStub(t)
	got, err := c.Convert(context.Background(), []byte("hello world"), "docx")
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if !strings.Contains(got, `hint="docx"`) {
		t.Errorf("hint not passed through: %q", got)
	}
	if !strings.Contains(got, "bytes=11") {
		t.Errorf("stdin not delivered intact: %q", got)
	}
}

// The exit-code table is a contract split across two languages. If someone
// renumbers rust/src/main.rs without touching errors.go, this catches it --
// provided the stub is regenerated to match.
func TestExitCodeMapping(t *testing.T) {
	c := newStub(t)
	cases := []struct {
		trigger string
		want    error
	}{
		{"TRIGGER_UNSUPPORTED", ErrUnsupported},
		{"TRIGGER_MALFORMED", ErrMalformed},
		{"TRIGGER_ENCRYPTED", ErrEncrypted},
		{"TRIGGER_RESOURCE_LIMIT", ErrResourceLimit},
		{"TRIGGER_MISSING_PART", ErrMissingPart},
		{"TRIGGER_UNKNOWN_CODE", ErrWASM},
	}
	for _, tc := range cases {
		t.Run(tc.trigger, func(t *testing.T) {
			_, err := c.Convert(context.Background(), []byte(tc.trigger), "")
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			var ce *ConvertError
			if !errors.As(err, &ce) {
				t.Fatalf("error is not *ConvertError: %T", err)
			}
			if ce.Detail == "" {
				t.Error("guest stderr was dropped")
			}
		})
	}
}

// This is the failure I kept warning about: a wasip1 command module carries a
// name, and wazero rejects instantiating the same name twice. Without
// WithName("") in Convert, this test deadlocks or errors under -race.
func TestConcurrentConvert(t *testing.T) {
	c := newStub(t, WithConcurrency(8))
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Convert(context.Background(), []byte("concurrent"), "docx"); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent Convert: %v", err)
	}
}

func TestContextTimeout(t *testing.T) {
	c := newStub(t)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := c.Convert(ctx, []byte("TRIGGER_SLOW"), "")
	if err == nil {
		t.Fatal("expected timeout error")
	}
	// The guest sleeps 30s; WithCloseOnContextDone must cut it short.
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("context cancellation did not interrupt guest: took %v", elapsed)
	}
}

// A runaway guest must fail its own conversion without harming the host.
func TestMemoryLimitContained(t *testing.T) {
	// The limit must exceed the module's own declared minimum memory or
	// CompileModule rejects it outright. Go's wasip1 runtime declares 272
	// pages and needs headroom above that just to start; the real Rust module
	// compiles at 64 pages, so this figure says nothing about production
	// tuning -- see WithMemoryLimitPages for measurements on the real module.
	c := newStub(t, WithMemoryLimitPages(512)) // 32 MiB
	if _, err := c.Convert(context.Background(), []byte("TRIGGER_HOG"), ""); err == nil {
		t.Fatal("expected memory limit error")
	}
	// Host still usable afterwards.
	if _, err := c.Convert(context.Background(), []byte("still alive"), "txt"); err != nil {
		t.Fatalf("converter unusable after guest OOM: %v", err)
	}
}

func TestInputGuards(t *testing.T) {
	c := newStub(t, WithMaxInputBytes(100))

	if _, err := c.Convert(context.Background(), nil, ""); !errors.Is(err, ErrUnsupported) {
		t.Errorf("empty input: got %v", err)
	}
	big := make([]byte, 200)
	if _, err := c.Convert(context.Background(), big, ""); !errors.Is(err, ErrResourceLimit) {
		t.Errorf("oversized input: got %v", err)
	}
}

// Guest memory exhaustion reaches the host in two shapes depending on where
// it happens, and both have to be recognised: a caller who hits either one
// needs to be told about WithMemoryLimitPages, not handed a wasm stack trace.
func TestGuestOutOfMemoryDetection(t *testing.T) {
	oom := []string{
		"memory allocation of 2621440 bytes failed", // allocator abort, traps
		"read stdin: out of memory",                 // shim cannot buffer input, clean exit
	}
	for _, s := range oom {
		if !guestOutOfMemory(s) {
			t.Errorf("not recognised as out of memory: %q", s)
		}
	}

	notOOM := []string{
		"",
		"malformed document: bad central directory",
		"unsupported input: nothing matched",
		"missing required part: word/document.xml",
		// A document whose own guard tripped is a document problem, and must
		// keep mapping to ErrResourceLimit rather than being rewritten into
		// advice about the host's memory setting.
		"resource limit exceeded: declared size implausible",
	}
	for _, s := range notOOM {
		if guestOutOfMemory(s) {
			t.Errorf("wrongly treated as out of memory: %q", s)
		}
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	c := newStub(t)
	ctx := context.Background()
	if err := c.Close(ctx); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := c.Close(ctx); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}
