//! wasm32-wasip1 command module wrapping the `anydoc` crate.
//!
//! Protocol (kept deliberately dumb so the Go side never touches linear memory):
//!
//!   stdin       raw document bytes
//!   argv[1]     optional format hint, a bare extension like "docx" ("" = detect)
//!   stdout      UTF-8 Markdown on success
//!   stderr      human-readable error message on failure
//!   exit code   0 = ok, otherwise maps to ConvertError::code()
//!
//! The exit codes are the ABI between this file and errors.go. Changing one is a
//! breaking change for go-anydoc; add new codes instead of renumbering.

use anydoc::{Format, to_markdown_bytes};
use std::io::{Read, Write};

const EXIT_UNSUPPORTED: i32 = 2;
const EXIT_MALFORMED: i32 = 3;
const EXIT_ENCRYPTED: i32 = 4;
const EXIT_RESOURCE_LIMIT: i32 = 5;
const EXIT_MISSING_PART: i32 = 6;
const EXIT_IO: i32 = 7;
const EXIT_BAD_HINT: i32 = 8;

/// `ConvertError::code()` returns stable identifiers that the Node and wasm
/// bindings also publish as `error.code`; map them to exit codes. `ConvertError`
/// is `#[non_exhaustive]`, so unknown codes must fall through to EXIT_IO rather
/// than panicking: an upstream bump that adds a variant then degrades instead
/// of breaking.
fn exit_code(code: &str) -> i32 {
    match code {
        "unsupported" => EXIT_UNSUPPORTED,
        "malformed" => EXIT_MALFORMED,
        "encrypted" => EXIT_ENCRYPTED,
        "resourceLimit" => EXIT_RESOURCE_LIMIT,
        "missingPart" => EXIT_MISSING_PART,
        _ => EXIT_IO,
    }
}

fn fail(code: i32, msg: &str) -> ! {
    let _ = std::io::stderr().write_all(msg.as_bytes());
    std::process::exit(code);
}

fn main() {
    let mut bytes = Vec::new();
    if let Err(e) = std::io::stdin().read_to_end(&mut bytes) {
        fail(EXIT_IO, &format!("read stdin: {e}"));
    }

    // Empty hint means "detect from content". Signature-less formats (CSV) can
    // only be reached by naming them, which is why the hint exists at all.
    let hint = std::env::args().nth(1).unwrap_or_default();
    let format: Option<Format> = if hint.is_empty() {
        None
    } else {
        match Format::from_extension(&hint) {
            Some(f) => Some(f),
            None => fail(EXIT_BAD_HINT, &format!("unknown format hint: {hint}")),
        }
    };

    match to_markdown_bytes(&bytes, format) {
        Ok(md) => {
            if let Err(e) = std::io::stdout().write_all(md.as_bytes()) {
                fail(EXIT_IO, &format!("write stdout: {e}"));
            }
        }
        Err(e) => fail(exit_code(e.code()), &e.to_string()),
    }
}
