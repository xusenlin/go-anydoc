package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	anydoc "github.com/xusenlin/go-anydoc"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: convert <file>")
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, anydoc.Info())

	c, err := anydoc.New()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ctx := context.Background()
	defer c.Close(ctx)

	f, err := os.Open(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer f.Close()

	// Name the format from the extension. Content detection would cover most
	// of these anyway, but CSV carries no signature and can only be reached by
	// naming it -- which a CLI that already has the filename may as well do.
	hint := strings.TrimPrefix(strings.ToLower(filepath.Ext(os.Args[1])), ".")

	md, err := c.ConvertReader(ctx, f, hint)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Print(md)
}
