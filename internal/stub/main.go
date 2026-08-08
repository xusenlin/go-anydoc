//go:build wasip1

// Command stub is a stand-in for the real anydoc wasm, built with Go's own
// wasip1 target. It implements the exact ABI declared in rust/src/main.rs so
// the Go harness -- stdio wiring, exit-code mapping, concurrency, timeouts --
// can be tested without a Rust toolchain in the loop.
//
// It is NOT a converter. Tests that assert real conversion output need the
// real module; see testdata/README.md.
//
//	go build -o testdata/stub.wasm ./internal/stub
//	(with GOOS=wasip1 GOARCH=wasm)
package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

func main() {
	doc, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprint(os.Stderr, "read stdin failed")
		os.Exit(7)
	}

	hint := ""
	if len(os.Args) > 1 {
		hint = os.Args[1]
	}

	// Magic inputs let tests drive every branch of the error mapping.
	switch strings.TrimSpace(string(doc)) {
	case "TRIGGER_UNSUPPORTED":
		fmt.Fprint(os.Stderr, "unsupported input: nothing matched")
		os.Exit(2)
	case "TRIGGER_MALFORMED":
		fmt.Fprint(os.Stderr, "malformed document: bad central directory")
		os.Exit(3)
	case "TRIGGER_ENCRYPTED":
		fmt.Fprint(os.Stderr, "document is encrypted")
		os.Exit(4)
	case "TRIGGER_RESOURCE_LIMIT":
		fmt.Fprint(os.Stderr, "resource limit exceeded: declared size implausible")
		os.Exit(5)
	case "TRIGGER_MISSING_PART":
		fmt.Fprint(os.Stderr, "missing required part: word/document.xml")
		os.Exit(6)
	case "TRIGGER_UNKNOWN_CODE":
		fmt.Fprint(os.Stderr, "from a newer module than this package knows")
		os.Exit(99)
	case "TRIGGER_SLOW":
		// Long enough that a test timeout fires first, exercising
		// WithCloseOnContextDone.
		time.Sleep(30 * time.Second)
	case "TRIGGER_HOG":
		// Grow until the guest memory limit traps, so the test can assert that
		// a runaway document surfaces as an error rather than taking the host
		// process down with it.
		var chunks [][]byte
		for {
			chunks = append(chunks, make([]byte, 1<<20))
		}
	}

	fmt.Fprintf(os.Stdout, "# stub\n\nhint=%q\nbytes=%d\n", hint, len(doc))
}
