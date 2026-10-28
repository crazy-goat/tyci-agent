// Package atomicfile replaces a file so that a reader sees either the old
// content or the new content, never a part of both. It depends on the
// standard library only, so any package that writes user notes can use it.
package atomicfile

import (
	"errors"
	"os"
	"path/filepath"
)

// Write stores data in path. The data goes to a temporary file in the same
// directory, which is renamed over path. The mode of the result is always
// 0600, even when path already existed with a looser mode: os.CreateTemp
// creates the temporary file with that mode, and the rename keeps it.
//
// The directory of path must exist. A failed write leaves no temporary file.
func Write(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}
