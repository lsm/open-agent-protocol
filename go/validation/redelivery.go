package validation

import "github.com/lsm/open-agent-protocol/go/protocol"

func (s *state) redeliveryHeld(held []protocol.ShutdownQueuedRun) {
	groups := map[protocol.SessionID][]protocol.RunID{}
	for _, entry := range held {
		groups[entry.SessionID] = append(groups[entry.SessionID], entry.RunID)
	}
	for session, ids := range groups {
		st := s.track(session)
		st.held, st.heldKnown = ids, true
	}
}

func (s *state) redeliveryResponse(i, line int, e protocol.Envelope, p protocol.SessionState) {
	st := s.track(p.SessionID)
	var records []protocol.RedeliveredRun
	if p.Recovery != nil {
		records = p.Recovery.RedeliveredRuns
	}
	if p.Recovery == nil || !p.Recovery.Recovered {
		if len(records) > 0 {
			s.add(CodeSessionStateMismatch, i, line, e, "/payload/recovery", "redelivery requires a recovered session")
		}
		return
	}
	var requested protocol.SessionOpenRequest
	if req := s.requests[e.InReplyTo]; req != nil && req.typ == protocol.TypeSessionOpenRequest {
		_ = req.envelope.DecodePayload(&requested)
	}
	restarting := requested.Reopen || len(records) > 0
	if !restarting {
		return
	}
	held := st.held
	if !st.heldKnown {
		for _, id := range st.order {
			if r := s.runs[id]; r != nil && !r.terminal && !r.started {
				held = append(held, id)
			}
		}
	}
	bad := false
	if st.redeliveryRoots == nil {
		st.redeliveryRoots = map[protocol.RunID]protocol.RunID{}
		st.redeliveryHeads = map[protocol.RunID]protocol.RunID{}
		st.redeliveryClosed = map[protocol.RunID]bool{}
	}
	if st.heldKnown || len(held) > 0 {
		if len(held) != len(records) {
			bad = true
		}
		for n, id := range held {
			if n >= len(records) || records[n].PreviousRunID != id {
				bad = true
			}
		}
	}
	previous, roots := map[protocol.RunID]bool{}, map[protocol.RunID]bool{}
	positions := map[protocol.RunID]int{}
	paired := map[protocol.RunID]bool{}
	for _, entry := range records {
		if !entry.Withdrawn {
			paired[entry.RunID] = true
		}
	}
	messageRuns := 0
	for n, entry := range p.ActiveRuns {
		if entry.Status == protocol.RunQueued {
			positions[entry.RunID] = n + 1
		}
		if restarting && !paired[entry.RunID] {
			if requested.Message != nil && s.runs[entry.RunID] == nil {
				_, knownRoot := st.redeliveryHeads[entry.RunID]
				if st.redeliveryClosed[entry.RunID] || knownRoot {
					bad = true
				}
				messageRuns++
				if messageRuns > 1 {
					bad = true
				}
				st.redeliveryRoots[entry.RunID] = entry.RunID
				st.redeliveryHeads[entry.RunID] = entry.RunID
			} else if entry.Status == protocol.RunQueued {
				bad = true
			}
		}
	}
	last := 0
	priorOrder := -1
	for _, entry := range records {
		prev, root, next := entry.PreviousRunID, entry.RootRunID, entry.RunID
		if previous[prev] || roots[root] || st.redeliveryClosed[prev] {
			bad = true
		}
		previous[prev], roots[root] = true, true
		if head, known := st.redeliveryHeads[root]; known && head != prev {
			bad = true
		}
		expected := st.redeliveryRoots[prev]
		if r := s.runs[prev]; r != nil {
			if r.order <= priorOrder {
				bad = true
			}
			priorOrder = r.order
			if r.session != p.SessionID || r.started {
				bad = true
			}
			if expected == "" && !r.recovered {
				expected = prev
			}
		}
		if expected != "" && root != expected {
			bad = true
		}
		for _, active := range p.ActiveRuns {
			if active.RunID == prev || active.RunID == root {
				bad = true
			}
		}
		if !entry.Withdrawn {
			position := positions[next]
			_, knownRoot := st.redeliveryHeads[next]
			if s.runs[next] != nil || st.redeliveryClosed[next] || knownRoot || position == 0 || position <= last {
				bad = true
			}
			last = position
		}
	}
	for _, entry := range records {
		if previous[entry.RootRunID] && entry.RootRunID != entry.PreviousRunID {
			bad = true
		}
	}
	if bad {
		s.add(CodeSessionStateMismatch, i, line, e, "/payload/recovery/redelivered_runs", "redeliveries must pair held carriers in order, preserve their roots and name fresh queued runs")
	}
	for _, id := range held {
		if r := s.runs[id]; r != nil && !r.started {
			r.terminal, r.terminalAt = true, i
		}
	}
	for _, entry := range records {
		st.redeliveryClosed[entry.PreviousRunID] = true
		st.redeliveryHeads[entry.RootRunID] = entry.RunID
		if !entry.Withdrawn {
			st.redeliveryRoots[entry.RunID] = entry.RootRunID
		}
	}
	st.held, st.heldKnown = nil, false
}
