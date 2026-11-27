package meltysynth

import (
	"errors"
	"io"
)

// Since modulators will not be supported, we discard the data.
func discardModulatorData(r io.Reader, size int32) error {
	if size < 0 || size%10 != 0 {
		return errors.New("the modulator list is invalid")
	}

	// Discard without allocating a size-sized buffer.
	n, err := io.CopyN(io.Discard, r, int64(size))
	if err != nil {
		return err
	}
	if n != int64(size) {
		return errors.New("failed to read the modulator list")
	}

	return nil
}
