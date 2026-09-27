package main

import (
	"errors"
	"flag"
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
	pinned, err := providercatalog.LoadPinned(providers.Files)
	if err != nil {
		return err
	}
	findings := append(providercatalog.Check(catalog), providercatalog.CheckLiterals(tree, catalog)...)
	findings = append(findings, providercatalog.CheckPinned(catalog, pinned)...)
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

func runCatalogURLs(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("providers catalog-urls", flag.ContinueOnError)
	fs.SetOutput(stderr)
	format := fs.String("format", "human", "output format: human or json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || (*format != "human" && *format != "json") {
		return errors.New("providers catalog-urls accepts only --format=human|json")
	}
	catalog, err := providercatalog.Load(providers.Files)
	if err != nil {
		return err
	}
	pinned := providercatalog.PinnedFrom(catalog)
	if *format == "json" {
		encoded, err := providercatalog.EncodePinned(catalog)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(stdout, "%s\n", encoded)
		return err
	}
	for _, endpoint := range pinned.Endpoints {
		fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\t%s\t%s\n", endpoint.ID, endpoint.Wire, endpoint.Region, endpoint.BaseURL, endpoint.ModelsURL, endpoint.RequestURL)
	}
	return nil
}
