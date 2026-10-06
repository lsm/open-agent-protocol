//go:build !unix

package main

import (
	"os"
	"path/filepath"
)

func lockSessionHistory(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return os.OpenFile(path+".lock", os.O_RDWR|os.O_CREATE, 0o600)
}
