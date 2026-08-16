package proc

import "io"

// wrapPipe — тут дедлайни працюють нативно.
func wrapPipe(r io.Reader) io.Reader { return r }
