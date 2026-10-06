package game

import (
	"time"

	"go.uber.org/zap"
	"zone-online/zone-server/internal/ai"
	"zone-online/zone-server/internal/config"
	"zone-online/zone-server/internal/network"
	"zone-online/zone-server/internal/protocol"
)

// aiCombatConfig is the resolved combat FSM configuration for this server.
type aiCombatConfig struct {
	Enabled bool
	Params  ai.CombatParams
}

// resolveAICombat resolves the ai_combat_* keys with documented defaults.
func resolveAICombat(cfg *config.Config) aiCombatConfig {
	enabled := true
	params := ai.CombatParams{
		AggroRadius:    ai.DefaultAggroRadius,
		AttackRange:    ai.DefaultAttackRange,
		AttackCooldown: ai.DefaultAttackCooldown,
		MeleeDamage:    ai.DefaultMeleeDamage,
		PatrolResume:   ai.DefaultPatrolResume,
	}
	if cfg == nil {
		return aiCombatConfig{Enabled: enabled, Params: params}
	}
	enabled = cfg.AICombatEnabledOrDefault()
	if cfg.AIAggroRadiusM > 0 {
		params.AggroRadius = float32(cfg.AIAggroRadiusM)
	}
	if cfg.AIAttackRangeM > 0 {
		params.AttackRange = float32(cfg.AIAttackRangeM)
	}
	if cfg.AIAttackCooldownMS > 0 {
		params.AttackCooldown = time.Duration(cfg.AIAttackCooldownMS) * time.Millisecond
	}
	if cfg.AIMeleeDamage > 0 {
		params.MeleeDamage = float32(cfg.AIMeleeDamage)
	}
	if cfg.AIPatrolResumeS > 0 {
		params.PatrolResume = time.Duration(cfg.AIPatrolResumeS) * time.Second
	}
	return aiCombatConfig{Enabled: enabled, Params: params}
}

// resolveAICorpseSeconds resolves ai_corpse_seconds (default 5).
func resolveAICorpseSeconds(cfg *config.Config) time.Duration {
	if cfg != nil && cfg.AICorpseSeconds > 0 {
		return time.Duration(cfg.AICorpseSeconds) * time.Second
	}
	return time.Duration(config.DefaultAICorpseSeconds) * time.Second
}

// aiHostileToPlayer is the combat FSM faction gate. Squads with an unset or
// "monster" faction are mutants/neutral and hostile to everyone; otherwise the
// persisted relation table decides (strictly negative = hostile, same faction
// is never hostile).
func aiHostileToPlayer(squadFaction, playerFaction string) bool {
	sf := normalizeFaction(squadFaction)
	if sf == "" || sf == "monster" {
		return true
	}
	pf := normalizeFaction(playerFaction)
	if pf != "" && sf == pf {
		return false
	}
	return RelationBetween(sf, pf) < 0
}

// aiSquadAllowedToFight vetoes engagement while the puppet itself stands in a
// server safe zone ("not in a safezone on either side").
func aiSquadAllowedToFight(sq *ai.Squad) bool {
	if sq == nil {
		return false
	}
	return CheckSafeZone(sq.Position[0], sq.Position[1], sq.Position[2], sq.Level) == nil
}

// combatTargets builds the per-tick target list from the session snapshot:
// alive, non-pending, same-level players outside safe zones. Faction/relation
// filtering happens in the FSM through aiHostileToPlayer.
func (s *Server) combatTargets(views []aiPlayerView, buf []ai.CombatTarget) []ai.CombatTarget {
	buf = buf[:0]
	for i := range views {
		v := &views[i]
		if v.health <= 0 || v.pendingCreate || v.inSafe {
			continue
		}
		buf = append(buf, ai.CombatTarget{
			ID:       v.id,
			Level:    v.level,
			Position: v.pos,
			Faction:  v.faction,
		})
	}
	return buf
}

// applyAIAttacks validates and delivers every melee swing produced by the FSM
// this tick. Damage goes through the same DamageHandler as player damage and is
// relayed to the victim as OpDamageNotify with AttackerID = the AI entity id.
func (s *Server) applyAIAttacks(attacks []ai.AIAttackEvent) {
	if s == nil || len(attacks) == 0 || s.damageHandler == nil {
		return
	}
	for i := range attacks {
		atk := &attacks[i]
		target := s.sessions.GetByID(atk.TargetID)
		if target == nil {
			continue
		}
		entityID, ok := s.aiRepl.entityID(atk.SquadID)
		if !ok {
			continue
		}
		proxy := &network.PlayerSession{
			SessionID:    entityID,
			CurrentLevel: atk.Level,
			Position:     atk.Position,
			Faction:      atk.Faction,
			Health:       100,
		}
		dmg := protocol.DamageNotify{
			TargetID:   atk.TargetID,
			AttackerID: entityID,
			Damage:     atk.Damage,
		}
		applied, valid, reason := s.damageHandler.ValidateAndApplyDamage(proxy, target, &dmg)
		if !valid {
			s.auditDamageRejected(entityID, reason)
			continue
		}
		out := dmg
		out.Damage = applied
		s.SendToSession(target, protocol.OpDamageNotify, protocol.FlagReliable, out)
		s.auditDamageAccepted(entityID, atk.TargetID, applied)
		if s.logger != nil {
			s.logger.Debug("AI melee applied",
				zap.Uint32("squad", atk.SquadID),
				zap.Uint32("attacker", entityID),
				zap.Uint32("target", atk.TargetID),
				zap.Float32("damage", applied),
			)
		}
		s.flushPlayerDeath(target)
	}
}

// flushPlayerDeath persists a zero-health player as dead. Used by the AI melee
// path (the player-vs-player path keeps its inline equivalent).
func (s *Server) flushPlayerDeath(sess *network.PlayerSession) {
	if s == nil || sess == nil {
		return
	}
	sess.Lock()
	uuid := sess.AccountID
	pos := sess.Position
	yaw := sess.Rotation[0]
	health := sess.Health
	sess.Unlock()
	if health > 0 || uuid == "" {
		return
	}
	s.SyncFlushPlayerTransform(uuid, pos[0], pos[1], pos[2], yaw, 0.0)
	if s.db == nil {
		return
	}
	if char, err := s.db.LoadCharacter(uuid); err == nil && char != nil {
		char.PosX, char.PosY, char.PosZ = pos[0], pos[1], pos[2]
		char.Yaw = yaw
		char.Health = 0
		char.Dead = 1
		_ = s.db.SaveCharacter(char)
	}
}

// handlePuppetDamage processes OpDamageNotify aimed at an AI entity id
// (>= AIEntityIDBase). The hit must pass the shared range/sanity/budget checks;
// the applied amount is subtracted from the puppet's health and, while alive,
// an updated ENTITY_ENTER is pushed to every session already tracking it (the
// OpAIState wire entry carries no health field). On death the squad enters the
// DEAD state and the replication tick streams AnimDeath, then releases the
// entity with ENTITY_LEAVE after ai_corpse_seconds.
func (s *Server) handlePuppetDamage(attacker *network.PlayerSession, attackerID uint32, dmg *protocol.DamageNotify) {
	if s == nil || s.aiRepl == nil || s.squads == nil || s.damageHandler == nil {
		return
	}
	squadID, ok := s.aiRepl.squadIDForEntity(dmg.TargetID)
	if !ok {
		return
	}
	state, ok := s.squads.GetPuppetState(squadID)
	if !ok || state.State == ai.AIStateDead {
		return
	}
	applied, valid, reason := s.damageHandler.ValidateAndApplyPuppetDamage(attacker, state.Level, state.Position, dmg)
	if !valid {
		s.auditDamageRejected(attackerID, reason)
		return
	}
	remHealth, dead, ok := s.squads.ApplyPuppetDamage(squadID, applied)
	if !ok {
		return
	}
	s.auditDamageAccepted(attackerID, dmg.TargetID, applied)
	if s.logger != nil {
		s.logger.Info("Damage applied to puppet",
			zap.Uint32("puppet", dmg.TargetID),
			zap.Uint32("attacker", attackerID),
			zap.Float32("damage", applied),
			zap.Float32("rem_health", remHealth),
			zap.Bool("dead", dead),
		)
	}
	if dead {
		// No loot yet (roadmap). The corpse stays for ai_corpse_seconds and
		// the replication tick broadcasts AnimDeath + ENTITY_LEAVE.
		return
	}
	s.broadcastPuppetHealth(squadID)
}

// broadcastPuppetHealth re-sends ENTITY_ENTER_AOI with the puppet's current
// health to every session currently tracking it, so the wire-visible health
// stays accurate after a non-lethal hit. OpAIState has no health field.
func (s *Server) broadcastPuppetHealth(squadID uint32) {
	state, ok := s.squads.GetPuppetState(squadID)
	if !ok {
		return
	}
	entityID, ok := s.aiRepl.entityID(squadID)
	if !ok {
		return
	}
	pkt := buildEntityEnterFromState(state, entityID)
	for _, sessID := range s.aiRepl.visibleSessions(squadID) {
		sess := s.sessions.GetByID(sessID)
		if sess == nil {
			continue
		}
		s.SendToSession(sess, protocol.OpEntityEnterAoI, protocol.FlagReliable, pkt)
	}
}

// buildEntityEnterFromState converts a puppet snapshot into the frozen
// ENTITY_ENTER_AOI v3 payload (132 bytes), matching aiEntityInfo.
func buildEntityEnterFromState(p ai.PuppetState, entityID uint32) protocol.EntityEnterAoI {
	info := aiEntityInfo(p, entityID)
	var pkt protocol.EntityEnterAoI
	pkt.EntityID = info.ID
	pkt.EntityType = info.Type
	copyNulTerm(pkt.Section[:], info.Section)
	pkt.PosX, pkt.PosY, pkt.PosZ = info.Pos[0], info.Pos[1], info.Pos[2]
	copyNulTerm(pkt.Faction[:], info.Faction)
	pkt.Health = uint8(info.Health)
	pkt.Gvid = info.Gvid
	copyNulTerm(pkt.Name[:], info.Name)
	return pkt
}
