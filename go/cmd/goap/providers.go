package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/lsm/open-agent-protocol/go/providercatalog"
	"github.com/lsm/open-agent-protocol/providers"
)

func checkProviders(stdout io.Writer) error {
	catalog, err := providercatalog.Load(providers.Files)
	if err != nil {
		return err
	}
	tree := os.DirFS(repositoryRoot())
	findings := append(providercatalog.Check(catalog), providercatalog.CheckLiterals(tree, catalog)...)
	if len(findings) > 0 {
		errs := make([]error, len(findings))
		for i, finding := range findings {
			errs[i] = finding
		}
		return errors.Join(errs...)
	}
	endpoints := 0
	for _, provider := range catalog.Providers {
		endpoints += len(provider.Endpoints)
	}
	fmt.Fprintf(stdout, "PASS providers: %d providers, %d endpoints\n", len(catalog.Providers), endpoints)
	return nil
}
