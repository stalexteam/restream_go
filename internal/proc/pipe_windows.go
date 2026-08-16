package proc

import "io"

// wrapPipe — exec-пайп Windows не pollable.
func wrapPipe(r io.Reader) io.Reader { return NewDeadlineReader(r) }
