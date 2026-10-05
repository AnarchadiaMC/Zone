package game

import (
	"fmt"
	"time"
)

// damageRejectAuditInterval rate-limits rejected-damage audit rows per attacker
// session. Rejections are kept for exploit forensics, but a forged-packet
// flood must not fill audit_log.
const damageRejectAuditInterval = 5 * time.Second

// auditDamageRejected records a rejected damage packet with its validation
// reason, at most once per damageRejectAuditInterval per attacker session.
func (s *Server) auditDamageRejected(attackerID uint32, reason string) {
	if s == nil {
		return
	}
	now := time.Now()
	s.damageAuditMu.Lock()
	if s.lastDamageRejectAudit == nil {
		s.lastDamageRejectAudit = make(map[uint32]time.Time)
	}
	if last, ok := s.lastDamageRejectAudit[attackerID]; ok && now.Sub(last) < damageRejectAuditInterval {
		s.damageAuditMu.Unlock()
		return
	}
	s.lastDamageRejectAudit[attackerID] = now
	s.damageAuditMu.Unlock()

	s.audit("", "damage_rejected", fmt.Sprintf("attacker=%d reason=%s", attackerID, reason))
}

// auditDamageAccepted records an applied hit with both session ids and the
// clamped damage amount actually applied.
func (s *Server) auditDamageAccepted(attackerID, targetID uint32, damage float32) {
	if s == nil {
		return
	}
	s.audit("", "damage_applied",
		fmt.Sprintf("attacker=%d target=%d damage=%.2f", attackerID, targetID, damage))
}

// clearDamageAudit drops a departed session's reject-audit throttle entry.
func (s *Server) clearDamageAudit(sessionID uint32) {
	if s == nil {
		return
	}
	s.damageAuditMu.Lock()
	delete(s.lastDamageRejectAudit, sessionID)
	s.damageAuditMu.Unlock()
}
