package ffgoconv

import (
	"errors"
	"io"

	"github.com/superwhiskers/crunch"
)

type CrunchMiniBuffer struct {
	*crunch.MiniBuffer
}

// Read implements io.Reader
func (minibuf *CrunchMiniBuffer) Read(p []byte) (n int, err error) {
	if len(p) == 0 {
		return 0, errors.New("cannot read into empty buffer")
	}

	length := minibuf.Len()

	if length == 0 {
		return 0, io.EOF
	}

	if length < 0 {
		return 0, errors.New("cannot read negative indice")
	}

	if length < len(p) {
		minibuf.ReadBytesNext(&p, int64(length))
		return length, nil
	}

	minibuf.ReadBytesNext(&p, int64(len(p)))
	return len(p), nil
}

// Write implements io.Writer
func (minibuf *CrunchMiniBuffer) Write(p []byte) (n int, err error) {
	if len(p) == 0 {
		return 0, errors.New("cannot write from empty buffer")
	}

	length := minibuf.Len()
	if length < len(p) {
		minibuf.Grow(int64(len(p) - length))
	}

	minibuf.WriteBytesNext(p)
	return len(p), nil
}

func (minibuf *CrunchMiniBuffer) Len() int {
	var length int64
	minibuf.AfterByte(&length)
	//fmt.Println(length)
	return int(length)
}
