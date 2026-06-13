package router

import (
	"github.com/sausheong/felix/internal/channel"
	"github.com/sausheong/felix/internal/config"
)

// Router matches inbound messages to agent IDs using binding rules.
type Router struct {
	bindings []config.Binding
	fallback string
}

// NewRouter creates a new message router.
func NewRouter(bindings []config.Binding, fallbackAgentID string) *Router {
	return &Router{
		bindings: bindings,
		fallback: fallbackAgentID,
	}
}

// Route returns the agent ID that should handle the given message.
// Matching priority: peer.id > peer.kind > accountId > channel > default.
func (r *Router) Route(msg channel.InboundMessage) string {
	const (
		rankNone = iota
		rankChannel
		rankAccount
		rankKind
		rankPeerID
	)
	bestRank := rankNone
	bestAgent := ""

	consider := func(rank int, agentID string) {
		if rank > bestRank {
			bestRank = rank
			bestAgent = agentID
		}
	}

	for _, b := range r.bindings {
		m := b.Match
		channelOK := m.Channel == "" || m.Channel == msg.Channel

		// Most specific: peer.id match
		if m.Peer != nil && m.Peer.ID != "" && m.Peer.ID == msg.SenderID && channelOK {
			consider(rankPeerID, b.AgentID)
		}

		// Peer kind match
		if m.Peer != nil && m.Peer.Kind != "" && m.Peer.Kind == string(msg.ChatType) && channelOK {
			consider(rankKind, b.AgentID)
		}

		// Account ID match
		if m.AccountID != "" && m.AccountID == msg.AccountID && channelOK {
			consider(rankAccount, b.AgentID)
		}

		// Channel match (least specific of the explicit matches)
		if m.Channel == msg.Channel && m.Peer == nil && m.AccountID == "" {
			consider(rankChannel, b.AgentID)
		}
	}

	if bestAgent != "" {
		return bestAgent
	}

	return r.fallback
}

// IsKnownPeer returns true if the given sender ID appears as a peer.id
// in any binding. This is used by the DM policy to determine whether a
// sender is "known" (has an explicit binding) or "unknown".
func (r *Router) IsKnownPeer(senderID string) bool {
	for _, b := range r.bindings {
		if b.Match.Peer != nil && b.Match.Peer.ID == senderID {
			return true
		}
	}
	return false
}
