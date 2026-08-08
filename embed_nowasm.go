//go:build anydoc_nowasm

package anydoc

// Built with -tags anydoc_nowasm: no module is compiled in, so the binary does
// not carry the ~5 MB payload. New() then requires WithWASM.
var embeddedWASM []byte

// EmbeddedAnydocVersion is empty in nowasm builds: the caller supplies the
// module, so this package cannot know its provenance.
const EmbeddedAnydocVersion = ""

const wasmEmbedded = false
