package header

import "io"

func Parse(in io.Reader) (out *Header, err error) {
	var h Header
	return &h, nil
}

type Header struct {
}
