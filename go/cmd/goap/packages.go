package main

import (
	"errors"
	"fmt"
	"go/build"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/lsm/open-agent-protocol/go/internal/publicset"
)

func checkGoPackages(stdout io.Writer) error {
	root := repositoryRoot()
	tree := os.DirFS(root)
	var findings []error
	public, internalCount, binaries := 0, 0, 0
	err := fs.WalkDir(tree, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			return nil
		}
		if name != "." && (entry.Name() == "testdata" || entry.Name() == "node_modules" || strings.HasPrefix(entry.Name(), ".")) {
			return fs.SkipDir
		}
		if name != "." {
			if _, err := fs.Stat(tree, path.Join(name, "go.mod")); err == nil {
				return fs.SkipDir
			}
		}
		pkg, err := build.ImportDir(filepath.Join(root, filepath.FromSlash(name)), 0)
		if err != nil {
			return nil
		}
		if pkg.Name == "main" {
			binaries++
			return nil
		}
		if publicset.Internal(name) {
			internalCount++
			return nil
		}
		if !publicset.Public(name) {
			findings = append(findings, fmt.Errorf("%s is importable and is in neither the public set nor go/internal, so every exported name in it is public API the day a Go program imports this module: name it or move it", name))
			return nil
		}
		public++
		return nil
	})
	if err != nil {
		return err
	}
	if len(findings) > 0 {
		return errors.Join(findings...)
	}
	fmt.Fprintf(stdout, "PASS go packages: %d public, %d internal, %d binaries\n", public, internalCount, binaries)
	return nil
}
