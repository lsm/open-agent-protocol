//go:build !unix

package main

import "os"

func lockSessionHistory(path string) (*os.File, error) {
	return os.OpenFile(path+".lock", os.O_RDWR|os.O_CREATE, 0o600)
}
