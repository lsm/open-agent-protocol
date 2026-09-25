package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/lsm/open-agent-protocol/go/adapter/acp"
	"github.com/lsm/open-agent-protocol/go/adapter/claude"
	"github.com/lsm/open-agent-protocol/go/adapter/codex/appserver"
	"github.com/lsm/open-agent-protocol/go/adapter/deepseek"
	"github.com/lsm/open-agent-protocol/go/adapter/hermes"
	"github.com/lsm/open-agent-protocol/go/adapter/opencode"
	"github.com/lsm/open-agent-protocol/go/adapter/pi"
	"github.com/lsm/open-agent-protocol/go/harness"
	"github.com/lsm/open-agent-protocol/harnesses"
)

var adapterPins = map[string]harness.AdapterPin{
	"acp":              {CapabilityRevision: acp.CapabilityRevision, Corpus: acp.CorpusDirectory},
	"claude-code":      {CapabilityRevision: claude.CapabilityRevision, Corpus: claude.CorpusDirectory},
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
