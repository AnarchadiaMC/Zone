package game

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"zone-online/zone-server/internal/network"
	"zone-online/zone-server/internal/protocol"
)

// Sentinel errors returned by GroupManager operations. The server maps them
// onto system chat replies to the affected player.
var (
	ErrGroupFull          = errors.New("group full")
	ErrGroupNoInvite      = errors.New("no pending group invite")
	ErrGroupAlreadyIn     = errors.New("already in a group")
	ErrGroupSelfInvite    = errors.New("cannot invite yourself")
	ErrGroupInvitePending = errors.New("target already has a pending invite")
)

// Group is one in-memory party. Members is join-ordered; Members[0] is not
// necessarily the leader (LeaderID is authoritative).
type Group struct {
	ID       uint32
	LeaderID uint32
	Members  []uint32
}

// GroupInvite is a pending invitation targeting one session. Invitations are
// keyed by target session ID and expire after the configured TTL.
type GroupInvite struct {
	InviterID  uint32
	TargetID   uint32
	TargetName string
	ExpiresAt  time.Time
}

// GroupManager owns all party state. It is deliberately independent from the
// wire layer: the server resolves nicknames and performs broadcasts.
type GroupManager struct {
	mu         sync.Mutex
	groups     map[uint32]*Group
	membership map[uint32]uint32
	invites    map[uint32]*GroupInvite
	nextID     uint32
	maxMembers int
	inviteTTL  time.Duration
}

// NewGroupManager creates a manager capped at maxMembers (clamped to the
// 8-member wire capacity) with the given invitation TTL.
func NewGroupManager(maxMembers int, inviteTTL time.Duration) *GroupManager {
	if maxMembers <= 0 {
		maxMembers = 4
	}
	if maxMembers > protocol.MaxGroupMembers {
		maxMembers = protocol.MaxGroupMembers
	}
	if inviteTTL <= 0 {
		inviteTTL = 60 * time.Second
	}
	return &GroupManager{
		groups:     make(map[uint32]*Group),
		membership: make(map[uint32]uint32),
		invites:    make(map[uint32]*GroupInvite),
		maxMembers: maxMembers,
		inviteTTL:  inviteTTL,
	}
}

// Invite records a pending invitation from inviter to target. A new invitation
// is rejected while an unexpired one is already pending for the target;
// expired entries are replaced. Capacity is enforced when the target accepts,
// so the invite target receives the group-full reply.
func (gm *GroupManager) Invite(inviter, target *network.PlayerSession) error {
	if inviter == nil || target == nil {
		return ErrGroupNoInvite
	}
	inviter.Lock()
	inviterID := inviter.SessionID
	inviter.Unlock()
	target.Lock()
	targetID := target.SessionID
	targetName := target.Name
	target.Unlock()

	if inviterID == 0 || targetID == 0 || inviterID == targetID {
		return ErrGroupSelfInvite
	}

	now := time.Now()
	gm.mu.Lock()
	defer gm.mu.Unlock()
	if existing, ok := gm.invites[targetID]; ok && now.Before(existing.ExpiresAt) {
		return ErrGroupInvitePending
	}
	gm.invites[targetID] = &GroupInvite{
		InviterID:  inviterID,
		TargetID:   targetID,
		TargetName: targetName,
		ExpiresAt:  now.Add(gm.inviteTTL),
	}
	return nil
}

// InviteFor returns a copy of the pending invitation targeting sessionID, or
// nil when none exists.
func (gm *GroupManager) InviteFor(sessionID uint32) *GroupInvite {
	gm.mu.Lock()
	defer gm.mu.Unlock()
	inv, ok := gm.invites[sessionID]
	if !ok {
		return nil
	}
	copied := *inv
	return &copied
}

// Decline removes and returns the pending invitation targeting the session.
func (gm *GroupManager) Decline(target *network.PlayerSession) *GroupInvite {
	if target == nil {
		return nil
	}
	target.Lock()
	targetID := target.SessionID
	target.Unlock()

	gm.mu.Lock()
	defer gm.mu.Unlock()
	inv, ok := gm.invites[targetID]
	if !ok {
		return nil
	}
	delete(gm.invites, targetID)
	copied := *inv
	return &copied
}

// Accept consumes the pending invitation for target. When the inviter is not
// already grouped, a new group is created with the inviter as leader.
func (gm *GroupManager) Accept(target *network.PlayerSession) (uint32, error) {
	if target == nil {
		return 0, ErrGroupNoInvite
	}
	target.Lock()
	targetID := target.SessionID
	target.Unlock()

	gm.mu.Lock()
	defer gm.mu.Unlock()

	inv, ok := gm.invites[targetID]
	if !ok {
		return 0, ErrGroupNoInvite
	}
	if time.Now().After(inv.ExpiresAt) {
		delete(gm.invites, targetID)
		return 0, ErrGroupNoInvite
	}
	delete(gm.invites, targetID)

	if _, ok := gm.membership[targetID]; ok {
		return 0, ErrGroupAlreadyIn
	}

	gid, ok := gm.membership[inv.InviterID]
	if !ok {
		gm.nextID++
		gid = gm.nextID
		gm.groups[gid] = &Group{ID: gid, LeaderID: inv.InviterID, Members: []uint32{inv.InviterID}}
		gm.membership[inv.InviterID] = gid
	}
	g := gm.groups[gid]
	if g == nil {
		return 0, ErrGroupNoInvite
	}
	if len(g.Members) >= gm.maxMembers {
		return 0, ErrGroupFull
	}
	g.Members = append(g.Members, targetID)
	gm.membership[targetID] = gid
	return gid, nil
}

// Leave removes the session from its group. It returns the group ID, the
// roster snapshot (all member session IDs before the change) and whether the
// session was a member. The group dissolves when the leader leaves or when
// only one member would remain; remaining members are dropped from the
// membership map in that case. The roster is used by the caller to notify
// every affected player, including the leaver with the empty state.
func (gm *GroupManager) Leave(sess *network.PlayerSession) (uint32, []uint32, bool) {
	if sess == nil {
		return 0, nil, false
	}
	sess.Lock()
	sessionID := sess.SessionID
	sess.Unlock()

	gm.mu.Lock()
	defer gm.mu.Unlock()
	gid, ok := gm.membership[sessionID]
	if !ok {
		return 0, nil, false
	}
	g := gm.groups[gid]
	delete(gm.membership, sessionID)
	if g == nil {
		return gid, []uint32{sessionID}, true
	}
	roster := append([]uint32(nil), g.Members...)

	remaining := g.Members[:0]
	for _, m := range g.Members {
		if m != sessionID {
			remaining = append(remaining, m)
		}
	}
	g.Members = remaining

	if g.LeaderID == sessionID || len(g.Members) <= 1 {
		for _, m := range g.Members {
			delete(gm.membership, m)
		}
		delete(gm.groups, gid)
	}
	return gid, roster, true
}

// GroupID returns the session's group ID when the session is a member.
func (gm *GroupManager) GroupID(sessionID uint32) (uint32, bool) {
	gm.mu.Lock()
	defer gm.mu.Unlock()
	gid, ok := gm.membership[sessionID]
	return gid, ok
}

// Members returns a copy of the group's member session IDs.
func (gm *GroupManager) Members(groupID uint32) []uint32 {
	gm.mu.Lock()
	defer gm.mu.Unlock()
	g := gm.groups[groupID]
	if g == nil {
		return nil
	}
	return append([]uint32(nil), g.Members...)
}

// MemberSessions resolves the group's member IDs against the session manager,
// skipping members that have since disconnected.
func (gm *GroupManager) MemberSessions(groupID uint32, sessions *network.SessionManager) []*network.PlayerSession {
	ids := gm.Members(groupID)
	if sessions == nil {
		return nil
	}
	out := make([]*network.PlayerSession, 0, len(ids))
	for _, id := range ids {
		if sess := sessions.GetByID(id); sess != nil {
			out = append(out, sess)
		}
	}
	return out
}

// IsSameGroup reports whether both sessions are members of the same group.
func (gm *GroupManager) IsSameGroup(a, b uint32) bool {
	if gm == nil || a == 0 || b == 0 {
		return false
	}
	gm.mu.Lock()
	defer gm.mu.Unlock()
	ga, oka := gm.membership[a]
	gb, okb := gm.membership[b]
	return oka && okb && ga == gb
}

// StatePayload builds the OpGroupState payload for groupID. A group ID of 0
// (or an unknown group) yields the empty state.
func (gm *GroupManager) StatePayload(groupID uint32, sessions *network.SessionManager) protocol.GroupState {
	var state protocol.GroupState
	if groupID == 0 || sessions == nil {
		return state
	}
	gm.mu.Lock()
	g := gm.groups[groupID]
	if g == nil {
		gm.mu.Unlock()
		return state
	}
	ids := append([]uint32(nil), g.Members...)
	leaderID := g.LeaderID
	gm.mu.Unlock()

	count := 0
	for _, id := range ids {
		if count >= len(state.Members) {
			break
		}
		sess := sessions.GetByID(id)
		if sess == nil {
			continue
		}
		sess.Lock()
		state.Members[count].SessionID = sess.SessionID
		copyNulTerm(state.Members[count].Name[:], sess.Name)
		copyNulTerm(state.Members[count].Faction[:], sess.Faction)
		if sess.SessionID == leaderID {
			state.Members[count].IsLeader = 1
		}
		sess.Unlock()
		count++
	}
	state.MemberCount = uint8(count)
	return state
}

// Tick drops expired invitations.
func (gm *GroupManager) Tick(now time.Time) {
	if gm == nil {
		return
	}
	gm.mu.Lock()
	defer gm.mu.Unlock()
	for id, inv := range gm.invites {
		if now.After(inv.ExpiresAt) {
			delete(gm.invites, id)
		}
	}
}

// ForgetSession removes every pending invitation involving sessionID, whether
// as target or inviter, so a departed session leaves no invite behind for its
// full TTL. Group membership itself is handled by Leave.
func (gm *GroupManager) ForgetSession(sessionID uint32) {
	if gm == nil || sessionID == 0 {
		return
	}
	gm.mu.Lock()
	defer gm.mu.Unlock()
	for id, inv := range gm.invites {
		if inv == nil {
			delete(gm.invites, id)
			continue
		}
		if id == sessionID || inv.InviterID == sessionID {
			delete(gm.invites, id)
		}
	}
}

// sessionName reads a session nickname under its lock.
func sessionName(sess *network.PlayerSession) string {
	if sess == nil {
		return ""
	}
	sess.Lock()
	defer sess.Unlock()
	return sess.Name
}

// sessionIDOf reads a session ID under its lock.
func sessionIDOf(sess *network.PlayerSession) uint32 {
	if sess == nil {
		return 0
	}
	sess.Lock()
	defer sess.Unlock()
	return sess.SessionID
}

// handleChatCommand consumes a slash command from chat text. Unknown
// slash-commands get a system reply to the sender only; commands are never
// broadcast to other players.
func (s *Server) handleChatCommand(sess *network.PlayerSession, text string) {
	if s == nil || sess == nil {
		return
	}
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return
	}
	switch strings.ToLower(fields[0]) {
	case "/invite":
		if len(fields) != 2 {
			s.SendChatToSession(sess, "Usage: /invite <nickname>")
			return
		}
		target := s.sessions.GetByName(fields[1])
		if target == nil {
			s.SendChatToSession(sess, fmt.Sprintf("Player '%s' is not online.", fields[1]))
			return
		}
		if err := s.groups.Invite(sess, target); err != nil {
			if errors.Is(err, ErrGroupFull) {
				s.SendChatToSession(sess, "Your group is full.")
			} else if errors.Is(err, ErrGroupSelfInvite) {
				s.SendChatToSession(sess, "You cannot invite yourself.")
			} else if errors.Is(err, ErrGroupInvitePending) {
				s.SendChatToSession(sess, fmt.Sprintf("%s already has a pending invite.", sessionName(target)))
			} else {
				s.SendChatToSession(sess, "Group invitation failed.")
			}
			return
		}
		s.SendChatToSession(sess, fmt.Sprintf("Group invitation sent to %s.", sessionName(target)))
		s.sendGroupInvite(target, sess)
	case "/accept":
		s.groupAccept(sess)
	case "/decline":
		s.groupDecline(sess)
	case "/leave":
		gid, roster, wasMember := s.groups.Leave(sess)
		if !wasMember {
			s.SendChatToSession(sess, "You are not in a group.")
			return
		}
		s.notifyGroupRoster(gid, roster)
	case "/group":
		s.sendGroupState(sess)
	default:
		s.SendChatToSession(sess, fmt.Sprintf("Unknown command: %s", fields[0]))
	}
}

// groupAccept handles /accept and OpGroupResponse(accept=1).
func (s *Server) groupAccept(sess *network.PlayerSession) {
	if s == nil || sess == nil {
		return
	}
	inv := s.groups.InviteFor(sessionIDOf(sess))
	if inv == nil {
		s.SendChatToSession(sess, "No pending group invitation.")
		return
	}
	if s.sessions.GetByID(inv.InviterID) == nil {
		s.groups.Decline(sess)
		s.SendChatToSession(sess, "The inviter is no longer connected.")
		return
	}
	gid, err := s.groups.Accept(sess)
	if err != nil {
		switch {
		case errors.Is(err, ErrGroupFull):
			s.SendChatToSession(sess, "Group is full.")
		case errors.Is(err, ErrGroupAlreadyIn):
			s.SendChatToSession(sess, "You are already in a group.")
		default:
			s.SendChatToSession(sess, "No pending group invitation.")
		}
		return
	}
	s.broadcastGroupState(gid)
}

// groupDecline handles /decline and OpGroupResponse(accept=0).
func (s *Server) groupDecline(sess *network.PlayerSession) {
	if s == nil || sess == nil {
		return
	}
	inv := s.groups.Decline(sess)
	if inv == nil {
		s.SendChatToSession(sess, "No pending group invitation.")
		return
	}
	s.SendChatToSession(sess, "Invitation declined.")
	if inviter := s.sessions.GetByID(inv.InviterID); inviter != nil {
		s.SendChatToSession(inviter, fmt.Sprintf("%s declined your group invitation.", sessionName(sess)))
	}
}

// sendGroupInvite delivers OpGroupInviteNotify to the invite target.
func (s *Server) sendGroupInvite(target, inviter *network.PlayerSession) {
	if s == nil || target == nil || inviter == nil {
		return
	}
	var pkt protocol.GroupInviteNotify
	inviter.Lock()
	pkt.InviterSessionID = inviter.SessionID
	copyNulTerm(pkt.InviterName[:], inviter.Name)
	copyNulTerm(pkt.InviterFaction[:], inviter.Faction)
	inviter.Unlock()
	s.SendToSession(target, protocol.OpGroupInviteNotify, protocol.FlagReliable, pkt)
}
