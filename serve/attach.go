package serve

import (
	"context"
	"fmt"
	"strings"

	base "github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/protocol"
)

// AttachmentGate reports the capability or degradation refusal an attaching
// open owes, and nil when the endpoint's own disclosure admits the request.
// It returns the adapter's error type rather than a rendered refusal, so the
// answer a frontend composes and the one it relays out of Open both go through
// ControlRefusal and cannot drift.
//
// The adapter's own gate reaches the same verdict, but only after the daemon
// has already refused for its credential rule, which is the wrong actionable
// answer: a caller told to name an operator-configured source would keep
// trying against an endpoint that attaches nothing.
//
// A probe that fails is reported, not skipped. Skipping it would let the
// daemon's own constraint answer an open whose capability rung was never
// settled — the precedence this gate exists to establish, undone in the one
// case where the descriptor is unavailable — and "name a configured source" is
// the wrong thing to tell a caller whose endpoint may attach nothing at all.
// The open is not forwarded either way, so no wire-supplied command reaches an
// adapter behind an unread descriptor.
//
// It lives here rather than in either codec because it is one verdict about
// one descriptor, and a copy per frontend is the second-normalization fault
// this unit has already paid for twice: one reading in a route and another in
// the validator is how a frontend starts refusing what a trace validates.
// servestdio mirrors servehttp one-to-one, so a divergence here would be a
// parity break that parity_test.go cannot see — it compares op surfaces, not
// the admission each one performs.
func AttachmentGate(ctx context.Context, hub *Hub, name, revision string, request protocol.SessionOpenRequest) (string, error) {
	if len(request.ToolSources) == 0 {
		return "", nil
	}
	descriptor, err := hub.Probe(ctx, name)
	if err != nil {
		return "", err
	}
	// A revision the caller supplied is an exact precondition, and one it did
	// not supply is not a defect. The core profile is conditional in both
	// directions: a request "may set capability_revision", and "when supplied, it
	// is an exact precondition"; a request that omits it "is evaluated against
	// the current capabilities" and its successful response "should set the
	// revision used for admission". An unpinned open is how a caller says it does
	// not need deterministic admission, which is a thing a caller is allowed to
	// say.
	//
	// The check reads that way and only that way. A first attempt at it turned
	// "if supplied, must be current" into "must supply" and refused every
	// unpinned attaching open — a refusal the protocol does not authorize, for a
	// request it explicitly permits.
	//
	// It takes the revision alone rather than the envelope carrying it, because
	// the two frontends decode into different envelope positions and this is the
	// only member of either that the verdict reads.
	if revision != "" && revision != descriptor.CapabilityRevision {
		return "", &StaleRevisionError{Expected: descriptor.CapabilityRevision, Current: revision}
	}
	// The key is resolved across the descriptor's layers, because a valid
	// descriptor may publish it under one alone. Reading only the top level
	// would refuse every attachment such an endpoint can honour, and it is the
	// second normalization — one here, one in the validator — that lets a route
	// start refusing what a trace validates.
	support, advertised := descriptor.Capabilities.EffectiveSupport(protocol.FeatureToolSourcesAttach)
	affirmative := advertised && support.Level != "" && support.Level != protocol.SupportUnavailable
	// A key advertised for no session-open mode is one an open cannot elect,
	// which is the capability rung and not a constraint on any one source.
	if !affirmative || !support.DisclosesMode(protocol.ModeSessionOpen) {
		return "", &base.UnsupportedControlError{Feature: protocol.FeatureToolSourcesAttach, Reason: base.ControlUnadvertised}
	}
	if support.Level == protocol.SupportDegraded && !request.AllowsDegraded(protocol.FeatureToolSourcesAttach) {
		return "", &base.DegradedControlError{Feature: protocol.FeatureToolSourcesAttach}
	}
	return descriptor.CapabilityRevision, nil
}

// StaleRevisionError refuses a request whose cited revision is not the
// endpoint's current one. The core profile names the code and both details:
// a caller told only that it is stale cannot tell whether to refresh or to
// stop, and the pair says which.
type StaleRevisionError struct{ Expected, Current string }

func (e *StaleRevisionError) Error() string {
	return "the open cites a capability revision that is no longer current"
}

// AttachmentRefusal names the one tool source an open may not attach over the
// client-facing wire, and why.
type AttachmentRefusal struct {
	Source string
	Reason string
}

func (e *AttachmentRefusal) Error() string {
	return fmt.Sprintf("tool source %q: %s", e.Source, e.Reason)
}

// ResolveAttachments applies the daemon's credential rule to one open's
// attachment array.
//
// "Loopback, single-user" describes the transport, not the origin of a request
// on it: the daemon admits any request whose Host names an allowlisted
// hostname, so a page in the user's browser can reach it. With command and
// args accepted from the wire, such a request would execute a process as the
// daemon's user. A process attachment therefore names an
// operator-configured source by id only, and the daemon fills the command, the
// arguments, and the environment from its own registry entry; a
// wire-supplied command, args, or literal NAME=value environment is refused
// before the open is forwarded. The bare-NAME allowlist form is the only
// environment a wire caller may write, because the literal form is not the
// caller's own secret when the caller may be a webpage.
//
// The registry entry is authoritative for the published members too —
// display_name, protocol, endpoint — and a caller that states one differing
// from the operator's is refused. The open publishes what the operator
// configured, so a caller naming the id alone gets a fuller descriptor back
// than it sent, and never a different one.
//
// This is a binding rule, not a protocol rule, and the exemption is
// in-process, not non-HTTP: command, args, and a literal environment stay
// legal exactly where the sender is the daemon itself or an embedding of
// serve.Hub in the same process, which has no wire to cross and already runs
// with the daemon's own authority. Every frontend applies it. A stdio peer is
// a separate process on the far side of a pipe, so it is a wire caller and
// gets the wire rule — the daemon cannot tell which process opened the pipe
// or on whose behalf it speaks, and "the parent launched us" is not a
// statement about the sender.
//
// The spoofing half never depended on the network at all: a caller that could
// set display_name or endpoint on an operator-configured source mislabels the
// operator's MCP server in a catalog a user reads, whatever carried the
// request.
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
		// The id is looked up before anything is decided from the attachment,
		// including its kind. Dispatching on the caller's kind first read the
		// answer out of the question: a `local` attachment naming a configured id
		// was forwarded verbatim, never checked against the operator's entry at
		// all, and a `process` attachment naming a `local` entry took the
		// operator's `local` descriptor back under a request that said
		// `process`. A configured id is the operator's source, whatever kind the
		// caller claims it is.
		configured, ok := hub.Registry().ToolSource(attachment.ID)
		if !ok {
			if attachment.Kind == protocol.ToolSourceProcess {
				return nil, &AttachmentRefusal{Source: attachment.ID, Reason: "no tool source of that id is configured on this daemon"}
			}
			// Nothing to run and nothing configured to contradict: the adapter
			// decides whether it can attach a source of that kind.
			resolved = append(resolved, attachment)
			continue
		}
		// The registry entry is authoritative for every member it carries, not
		// only the three the daemon runs the source with. A caller that could set
		// display_name or endpoint on an operator-configured source would label
		// the operator's own MCP server in the catalog a user reads, which is a
		// spoof rather than a configuration; one that could set `kind` would
		// choose how that source is reached.
		//
		// A caller that states one anyway is refused rather than silently
		// overwritten. Substituting would leave a request and its response
		// disagreeing about the same source — the caller could not tell an
		// endpoint that honoured its attachment from one that changed it, which
		// is the fault this whole unit is built to make impossible, and the
		// validator diagnoses it on a trace assembled from the exchange. Naming
		// the id alone is the shape the route is for; repeating the operator's
		// own values is permitted because it contradicts nothing.
		//
		// The list is every member protocol.ToolSourceAttachment carries, less
		// the ones handled above: `id` is the lookup key and cannot disagree,
		// `command` and `args` are refused outright from the wire, and
		// `environment` is additive under the bare-NAME allowlist. Nothing else
		// exists to omit.
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

// mergeEnvironment adds the caller's allowlist entries to the operator's,
// keeping the operator's where both name one variable.
//
// The two lists resolve against different things. By the time it gets here the
// operator's entries are literal `NAME=value` pairs, resolved at load from the
// daemon's own environment; the caller's are bare names, which the adapter
// later resolves against *its* allowlist. Concatenating them put one variable
// in the array twice, with two values that can genuinely differ — and what a
// child does with a duplicate name is defined nowhere: not by ACP's schema, not
// by this protocol, not by the adapter. The environment is the last place to
// leave an outcome undefined, because the values in it are credentials.
//
// The wire schema was never going to catch it. `uniqueItems` compares strings,
// and `MCP_TOKEN=operator-secret` and `MCP_TOKEN` are two different strings
// naming one variable — so this is not a case of validation being skipped on
// the merged array, but of string uniqueness not being name uniqueness.
//
// The operator wins, and the caller's entry is dropped rather than the open
// refused. A caller cannot discover which names an operator configured: the
// published projection carries no environment at all, by design. So naming one
// defensively is a legitimate request the caller had no way to know was
// redundant, and refusing it would fail an open for a collision only the daemon
// can see. Dropping it satisfies the request exactly — the variable is present,
// with the value the operator chose — and an override the caller might have
// intended is refused by the same act, which is the outcome refusing would have
// produced anyway.
//
// This is deliberately not the reasoning the registry's own bare-`NAME` rule
// takes, and the difference is the point: there, dropping an unresolvable name
// starts the MCP server *without* its token, to fail later as though the server
// were broken. Here nothing is missing — a second request for a variable
// already supplied is answered by the one already there.
//
// A name the operator did not configure is still added, which is what makes the
// caller's list additive rather than decorative; its value comes from the
// adapter's own operator-configured allowlist and never from the wire.
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

// hasLiteralEnvironment reports whether any entry carries a literal value
// rather than the bare allowlist name.
func hasLiteralEnvironment(environment []string) bool {
	for _, entry := range environment {
		if strings.Contains(entry, "=") {
			return true
		}
	}
	return false
}
