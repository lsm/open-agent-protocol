package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/lsm/oxap/go/adapter/acp"
	"github.com/lsm/oxap/go/adapter/claude"
	"github.com/lsm/oxap/go/adapter/codex/appserver"
	"github.com/lsm/oxap/go/adapter/copilot"
	"github.com/lsm/oxap/go/adapter/deepseek"
	"github.com/lsm/oxap/go/adapter/hermes"
	"github.com/lsm/oxap/go/adapter/opencode"
	"github.com/lsm/oxap/go/adapter/pi"
	"github.com/lsm/oxap/go/harness"
	"github.com/lsm/oxap/harnesses"
)

var adapterPins = map[string]harness.AdapterPin{
	"acp":              {CapabilityRevision: acp.CapabilityRevision, Corpus: acp.CorpusDirectory},
	"claude-code":      {CapabilityRevision: claude.CapabilityRevision, Corpus: claude.CorpusDirectory},
	"copilot-cli":      {CapabilityRevision: copilot.CapabilityRevision, Corpus: copilot.CorpusDirectory},
	"codex-app-server": {CapabilityRevision: appserver.CapabilityRevision, Corpus: appserver.CorpusDirectory},
	"deepseek-harness": {CapabilityRevision: deepseek.CapabilityRevision, Corpus: deepseek.CorpusDirectory},
	"hermes":           {CapabilityRevision: hermes.CapabilityRevision, Corpus: hermes.CorpusDirectory},
	"opencode":         {CapabilityRevision: opencode.CapabilityRevision, Corpus: opencode.CorpusDirectory},
	"pi":               {CapabilityRevision: pi.CapabilityRevision, Corpus: pi.CorpusDirectory},
}

func checkHarnesses(stdout io.Writer) error {
	catalog, err := harness.Load(harnesses.Files)
	if err != nil {
		return err
	}
	tree := os.DirFS(repositoryRoot())
	findings := append(harness.Check(tree, catalog, adapterPins), harness.CheckLiterals(tree, catalog)...)
	if len(findings) > 0 {
		errs := make([]error, len(findings))
		for i, finding := range findings {
			errs[i] = finding
		}
		return errors.Join(errs...)
	}
	versions := 0
	for _, entry := range catalog.Harnesses {
		versions += len(entry.Versions)
	}
	fmt.Fprintf(stdout, "PASS harnesses: %d harnesses, %d versions\n", len(catalog.Harnesses), versions)
	return nil
}
