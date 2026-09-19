package serve

import (
	"context"
	"fmt"
	"strings"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

func AttachmentGate(ctx context.Context, hub *Hub, name, revision string, request protocol.SessionOpenRequest) (string, error) {
	if len(request.ToolSources) == 0 {
		return "", nil
	}
	descriptor, err := hub.Probe(ctx, name)
	if err != nil {
		return "", err
	}

	if revision != "" && revision != descriptor.CapabilityRevision {
		return "", &StaleRevisionError{Expected: descriptor.CapabilityRevision, Current: revision}
	}

	support, advertised := descriptor.Capabilities.EffectiveSupport(protocol.FeatureToolSourcesAttach)
	affirmative := advertised && support.Level != "" && support.Level != protocol.SupportUnavailable

	if !affirmative || !support.DisclosesMode(protocol.ModeSessionOpen) {
		return "", &base.UnsupportedControlError{Feature: protocol.FeatureToolSourcesAttach, Reason: base.ControlUnadvertised}
	}
	if support.Level == protocol.SupportDegraded && !request.AllowsDegraded(protocol.FeatureToolSourcesAttach) {
		return "", &base.DegradedControlError{Feature: protocol.FeatureToolSourcesAttach}
	}
	return descriptor.CapabilityRevision, nil
}

type StaleRevisionError struct{ Expected, Current string }

func (e *StaleRevisionError) Error() string {
	return "the open cites a capability revision that is no longer current"
}

type AttachmentRefusal struct {
	Source string
	Reason string
}

func (e *AttachmentRefusal) Error() string {
	return fmt.Sprintf("tool source %q: %s", e.Source, e.Reason)
}

func ResolveAttachments(hub *Hub, attachments []protocol.ToolSourceAttachment) ([]protocol.ToolSourceAttachment, *AttachmentRefusal) {
	if len(attachments) == 0 {
		return nil, nil
	}
	resolved := make([]protocol.ToolSourceAttachment, 0, len(attachments))
	for _, attachment := range attachments {
		switch {
		case attachment.Command != "" || len(attachment.Args) > 0:
			return nil, &AttachmentRefusal{Source: attachment.ID, Reason: "the daemon does not accept a command or arguments from the wire; name an operator-configured source by id"}
		case hasLiteralEnvironment(attachment.Environment):
			return nil, &AttachmentRefusal{Source: attachment.ID, Reason: "the daemon accepts only the bare NAME allowlist form in environment"}
		}

		configured, ok := hub.Registry().ToolSource(attachment.ID)
		if !ok {
			if attachment.Kind == protocol.ToolSourceProcess {
				return nil, &AttachmentRefusal{Source: attachment.ID, Reason: "no tool source of that id is configured on this daemon"}
			}

			resolved = append(resolved, attachment)
			continue
		}

		for _, member := range []struct{ name, wire, operator string }{
			{"kind", attachment.Kind, configured.Kind},
			{"display_name", attachment.DisplayName, configured.DisplayName},
			{"protocol", attachment.Protocol, configured.Protocol},
			{"endpoint", attachment.Endpoint, configured.Endpoint},
		} {
			if member.wire != "" && member.wire != member.operator {
				return nil, &AttachmentRefusal{
					Source: attachment.ID,
					Reason: fmt.Sprintf("the daemon does not accept %s from the wire for a configured source; name it by id", member.name),
				}
			}
		}
		configured.Environment = mergeEnvironment(configured.Environment, attachment.Environment)
		resolved = append(resolved, configured)
	}
	return resolved, nil
}

func mergeEnvironment(operator, caller []string) []string {
	merged := append([]string(nil), operator...)
	if len(caller) == 0 {
		return merged
	}
	configured := make(map[string]bool, len(operator))
	for _, entry := range operator {
		name, _, _ := strings.Cut(entry, "=")
		configured[name] = true
	}
	for _, entry := range caller {
		name, _, _ := strings.Cut(entry, "=")
		if configured[name] {
			continue
		}
		configured[name] = true
		merged = append(merged, entry)
	}
	return merged
}

func hasLiteralEnvironment(environment []string) bool {
	for _, entry := range environment {
		if strings.Contains(entry, "=") {
			return true
		}
	}
	return false
}
