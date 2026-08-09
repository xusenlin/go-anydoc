package anydoc

import (
	"errors"
	"fmt"
	"strings"
)

// Sentinel errors returned by Convert. Match them with errors.Is.
//
// The numeric codes behind these live in rust/src/main.rs and are part of the
// module ABI: a wasm built from a different revision of that file may not agree
// with this table. That is what EmbeddedAnydocVersion is for.
var (
	// ErrUnsupported means the input was not a format anydoc can parse, either
	// because the content signature matched nothing or the hint named a format
	// that does not exist.
	ErrUnsupported = errors.New("anydoc: unsupported format")

	// ErrMalformed means the format was recognised but the bytes were corrupt.
	ErrMalformed = errors.New("anydoc: malformed document")

	// ErrEncrypted means the document is password-protected. anydoc does not
	// decrypt; obtain a decrypted copy first.
	ErrEncrypted = errors.New("anydoc: document is encrypted")

	// ErrResourceLimit means a guard inside anydoc tripped, e.g. a zip bomb or
	// an implausible declared size. Distinct from the host-side memory limit,
	// which surfaces as ErrWASM.
	ErrResourceLimit = errors.New("anydoc: resource limit exceeded")

	// ErrMissingPart means a required part of a container format was absent,
	// e.g. a .docx with no word/document.xml.
	ErrMissingPart = errors.New("anydoc: missing required part")

	// ErrWASM means the guest trapped, ran out of memory, or otherwise failed
	// in a way that is not a document problem. Treat as an internal error.
	ErrWASM = errors.New("anydoc: wasm execution failed")

	// ErrNoWASM means no module was available: the build used the
	// anydoc_nowasm tag and no WithWASM option was supplied.
	ErrNoWASM = errors.New("anydoc: no wasm module (build without anydoc_nowasm, or pass WithWASM)")
)

// ConvertError carries the guest's stderr alongside a sentinel, so callers can
// branch on the class of failure and still surface anydoc's own message.
type ConvertError struct {
	Kind   error  // one of the sentinels above
	Detail string // guest stderr, may be empty
	Code   int    // raw exit code, for debugging
}

func (e *ConvertError) Error() string {
	if e.Detail == "" {
		return e.Kind.Error()
	}
	return fmt.Sprintf("%s: %s", e.Kind.Error(), e.Detail)
}

func (e *ConvertError) Unwrap() error { return e.Kind }

// exit codes, mirroring rust/src/main.rs
const (
	exitUnsupported   = 2
	exitMalformed     = 3
	exitEncrypted     = 4
	exitResourceLimit = 5
	exitMissingPart   = 6
	exitIO            = 7
	exitBadHint       = 8
)

// guestOutOfMemory reports whether the guest died allocating rather than
// failing on the document. It takes two shapes, same cause and same fix:
//
//   - "memory allocation of N bytes failed": Rust's allocation-failure handler
//     aborting mid-conversion, which reaches the host as an unreachable trap
//     with a wasm stack trace attached.
//   - "read stdin: out of memory": the shim could not even buffer the input,
//     so it exits cleanly with the io code instead of trapping.
//
// Both are worth naming, because neither raw form mentions the one setting
// that would fix it.
func guestOutOfMemory(stderr string) bool {
	if strings.Contains(stderr, "out of memory") {
		return true
	}
	return strings.Contains(stderr, "memory allocation of") &&
		strings.Contains(stderr, "failed")
}

// errGuestOutOfMemory explains the limit that was hit and how to raise it.
func errGuestOutOfMemory(pages uint32, stderr string) error {
	return &ConvertError{
		Kind: ErrWASM,
		Detail: fmt.Sprintf(
			"the guest ran out of memory at its %d page limit (%d MiB); "+
				"raise it with WithMemoryLimitPages, remembering that each "+
				"concurrent guest can claim that much (guest said: %s)",
			pages, pages/16, stderr),
	}
}

func errorForExit(code int, stderr string) error {
	var kind error
	switch code {
	case exitUnsupported, exitBadHint:
		kind = ErrUnsupported
	case exitMalformed:
		kind = ErrMalformed
	case exitEncrypted:
		kind = ErrEncrypted
	case exitResourceLimit:
		kind = ErrResourceLimit
	case exitMissingPart:
		kind = ErrMissingPart
	case exitIO:
		kind = ErrWASM
	default:
		// An unknown code means the wasm is newer than this package. Report it
		// rather than silently mapping it onto something plausible.
		kind = ErrWASM
	}
	return &ConvertError{Kind: kind, Detail: stderr, Code: code}
}
