package main

import (
	"errors"
	"fmt"
	"go/build"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

var publicGoPackages = map[string]bool{
	"go/client":              true,
	"go/harness":             true,
	"go/protocol":            true,
	"go/providercatalog":     true,
	"go/serve":               true,
	"go/serve/serveendpoint": true,
	"go/serve/servehttp":     true,
	"go/serve/servestdio":    true,
	"go/validation":          true,
}

func internalGoPackage(name string) bool {
	return name == "go/internal" || strings.HasPrefix(name, "go/internal/") || strings.Contains(name, "/internal/")
}

func publicGoPackage(name string) bool {
	if internalGoPackage(name) {
		return false
	}
	if name == "go/adapter" || strings.HasPrefix(name, "go/adapter/") {
		return true
	}
	return publicGoPackages[name]
}

func checkGoPackages(stdout io.Writer) error {
	root := repositoryRoot()
	tree := os.DirFS(root)
	var findings []error
	public, internalCount, binaries := 0, 0, 0
	err := fs.WalkDir(tree, "go", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			return nil
		}
		if entry.Name() == "testdata" || strings.HasPrefix(entry.Name(), ".") {
			return fs.SkipDir
		}
		pkg, err := build.ImportDir(filepath.Join(root, filepath.FromSlash(name)), 0)
		if err != nil {
			return nil
		}
		if pkg.Name == "main" {
			binaries++
			return nil
		}
		if internalGoPackage(name) {
			internalCount++
			return nil
		}
		if !publicGoPackage(name) {
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
