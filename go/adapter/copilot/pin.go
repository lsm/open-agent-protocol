package copilot

import "github.com/lsm/oxap/harnesses"

var pin = harnesses.Current("copilot-cli")

var (
	PinnedVersion      = pin.EndpointVersion
	CapabilityRevision = pin.CapabilityRevision
	CorpusDirectory    = pin.Corpus
)
