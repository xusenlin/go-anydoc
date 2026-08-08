//go:build !anydoc_nowasm

package anydoc

import _ "embed"

// The module is embedded by default so that `go get` + New() just works.
// Builds that would rather ship the wasm out of band use -tags anydoc_nowasm
// and supply it through WithWASM.
//
//go:embed anydoc.wasm
var embeddedWASM []byte

// EmbeddedAnydocVersion is the upstream crate version this wasm was built from.
// Kept in sync by .github/workflows/build-wasm.yml.
const EmbeddedAnydocVersion = "0.1.7"

const wasmEmbedded = true
