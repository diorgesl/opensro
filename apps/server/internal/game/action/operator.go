/*
===========================================================================

operator.go - operator diagnostics and recovery through character authority

Recovery runs under an exclusive transport control lease supplied by the
composition root. It uses authored town destinations and ordinary persistence;
it never grants GM privileges, spends items, revives a corpse, or edits SQL.
The stat reset is the one that changes a character's build: it keeps every
earned point and only returns the spent ones to the free pool.

===========================================================================
*/
package action

import (
	"fmt"
	"sort"
	"strings"

	"opensro.online/server/internal/domain"
	"opensro.online/server/internal/game/pk"
	"opensro.online/server/internal/game/progression"
	"opensro.online/server/internal/game/world/simulation"
)

/*
================
OperatorTown
================
*/
type OperatorTown struct {
	ID       uint32           `json:"id"`
	Code     string           `json:"code"`
	Position simulation.Spawn `json:"position"`
}

/*
================
OperatorTowns

Only authored outdoor recall gates qualify, matching appointedRebirthPoint.
The portal building flag includes city gates; it does not mean an indoor region.
Client coordinates are never input.
================
*/
func (rt *Runtime) OperatorTowns() []OperatorTown {
	rows := []OperatorTown{}
	if rt.portals == nil {
		return rows
	}
	for _, destination := range rt.portals.destinations {
		if destination.recall && destination.ref != 0 && destination.spawn.RegionID != 0 && simulation.PlaneKindOf(destination.spawn.RegionID) == simulation.PlaneOutdoor {
			rows = append(rows, OperatorTown{destination.id, destination.code, destination.spawn})
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return rows
}

/*
================
OperatorCharacter

Explicit projection excludes account IDs, authentication material and chat.
================
*/
func (rt *Runtime) OperatorCharacter(division, name string) (map[string]any, error) {
	unlock := rt.lockDivision(division)
	defer unlock()
	c := rt.characterSnapshot(division, rt.findCharacter(division, name))
	if c == nil {
		return nil, fmt.Errorf("character not found")
	}
	world := rt.Worlds.Snapshot(simulation.WorldKey(division, c.Name), func() simulation.WorldState { return simulation.SeedWorldState(c) })
	walk, run := world.MovementSpeeds()
	return map[string]any{"id": c.ID, "name": c.Name, "level": c.Level, "gold": goldOf(c), "hp": c.CurrentHP, "mp": c.CurrentMP,
		"savedWorld": c.World, "liveWorld": world, "teleportMode": c.NativeTeleportMode,
		"movementSpeeds": OperatorSpeeds{Walk: walk, Run: run}, "effects": rt.operatorEffects(division, c.Name),
		"bodyStatus": c.NativeBodyStatus, "companions": c.Companions(), "inventory": c.MissionInventory,
		"pk": c.PK, "pvpState": c.PVPState(), "aggressions": c.Aggressions,
		"strength": domain.CharacterStrength(c), "intellect": domain.CharacterIntellect(c), "statPoints": c.StatPoints,
		"skillPoints": c.SkillPoints, "experience": c.Experience, "maxLevel": c.MaxLevel,
		"premiumClock": c.PremiumClock, "paramJobs": c.ParamJobs, "compositeJobs": c.CompositeJobs,
		"timedSkillJobs": c.TimedSkillJobs}, nil
}

/*
================
OperatorSpeeds

The walk and run speeds the server's mover times paths with (WorldState
keeps them out of its JSON). A client running at another speed rubber-bands,
so a speed report needs both sides.
================
*/
type OperatorSpeeds struct {
	Walk float32 `json:"walk"`
	Run  float32 `json:"run"`
}

/*
================
OperatorEffect

One installed status effect, enough to tell which buff a speed comes from.
================
*/
type OperatorEffect struct {
	SkillID         uint32 `json:"skillId"`
	Token           uint32 `json:"token"`
	State           uint8  `json:"state"`
	MovementPercent uint32 `json:"movementPercent,omitempty"`
	Persistent      bool   `json:"persistent,omitempty"`
	StopRequested   bool   `json:"stopRequested,omitempty"`
	ExpiresAtMs     int64  `json:"expiresAtMs,omitempty"`
}

/*
================
operatorEffects
================
*/
func (rt *Runtime) operatorEffects(division, name string) []OperatorEffect {
	rows := []OperatorEffect{}
	for _, effect := range rt.effects.Snapshot(division, name) {
		rows = append(rows, OperatorEffect{SkillID: effect.SkillID, Token: effect.InstanceToken, State: uint8(effect.State),
			MovementPercent: effect.MovementPercent, Persistent: effect.Persistent, StopRequested: effect.StopRequested,
			ExpiresAtMs: effect.ExpiresAtMs})
	}
	return rows
}

/*
================
OperatorClearPK

Operator recovery is port-only, not native, and disabled by default unless
the dedicated operator credential is configured. The caller holds the
character's transport control lease.
Daily and total history remain intact, including the daily attack cap. Repair
and relief share one authority update so no intermediate red state is visible.
The normal penalty owner supplies the keeper deadline; no live frames are sent
to the evicted session. Entry publishes the resulting state on reconnect.
================
*/
func (rt *Runtime) OperatorClearPK(division, name string) error {
	unlock := rt.lockDivision(division)
	defer unlock()
	c := rt.findCharacter(division, name)
	if c == nil {
		return fmt.Errorf("character not found")
	}
	if !rt.deps.Update(c, "operator-clear-pk", func() bool {
		if c.DeletePending {
			return false
		}
		if c.PK != nil {
			now := rt.Now()
			pk.RepairKeeper(c, now)
			pk.AddPenalty(c, -pk.MaxPenalty, now)
		}
		c.Aggressions = nil
		return true
	}) {
		return fmt.Errorf("character PK clear refused")
	}
	rt.aggressionActors.Delete(simulation.WorldKey(division, c.Name))
	rt.notePKRecord(division, c)
	return nil
}

/*
================
OperatorClearPremium

Port-only, not native: the operator ends a character's premium, under the
same disabled-by-default credential and transport control lease as the other
recoveries. It removes the premium ticket keepers (EXP, skill EXP, kept death
EXP), the daily allotment clock, the premium package's limited item and
quest uses, and the package's persistent option buff (its USU1 skill, saved
as a timed skill job and re-installed at login). Ordinary EXP scrolls and a
package's stall decoration stay. The session is gone, so no param-job or
buff end frame is sent; entry publishes the board on reconnect. Clearing a
character without premium changes nothing.
================
*/
func (rt *Runtime) OperatorClearPremium(division, name string) error {
	unlock := rt.lockDivision(division)
	defer unlock()
	c := rt.findCharacter(division, name)
	if c == nil {
		return fmt.Errorf("character not found")
	}
	if !rt.deps.Update(c, "operator-clear-premium", func() bool {
		if c.DeletePending {
			return false
		}
		var jobs []domain.ParamJob
		for _, job := range c.ParamJobs {
			switch job.Param {
			case paramPremiumExpRate, paramPremiumSkillExpRate, paramDeathExpKept:
				continue
			}
			jobs = append(jobs, job)
		}
		var works []domain.CompositeJob
		for _, work := range c.CompositeJobs {
			if work.Kind == domain.CompositeUsedItemLimit || work.Kind == domain.CompositeUsedQuestLimit {
				continue
			}
			works = append(works, work)
		}
		var skillJobs []domain.TimedSkillJob
		for _, job := range c.TimedSkillJobs {
			if rt.premiumPackageSkill(job.SkillID) {
				continue
			}
			skillJobs = append(skillJobs, job)
		}
		c.ParamJobs, c.CompositeJobs, c.PremiumClock, c.TimedSkillJobs = jobs, works, nil, skillJobs
		return true
	}) {
		return fmt.Errorf("character premium clear refused")
	}
	return nil
}

// premiumPackageSkillPrefix names the premium packages' option buffs: every
// shipped USU1 entry is one of them (SKILL_MALL_PRE_APRU_4W_01, _GLOBAL_4W_01,
// _GLOBAL_4W_PLUS_01), and only the Gold/Silver Time packages carry USU1.
// INFERENCE: the item references cannot be enumerated to derive the set from
// the packages, so the authored "PRE" (premium) family names it.
const premiumPackageSkillPrefix = "SKILL_MALL_PRE_"

/*
================
premiumPackageSkill
================
*/
func (rt *Runtime) premiumPackageSkill(id uint32) bool {
	if rt.deps.SkillData() == nil {
		return false
	}
	row, ok := rt.deps.SkillData().SkillByID(id)
	return ok && strings.HasPrefix(row.Codename, premiumPackageSkillPrefix)
}

/*
================
OperatorResetStats

Port-only, not native: the operator's stat reset, under the same disabled-by-
default credential and transport control lease as the other recoveries.
STR and INT return to their value at the character's level with nothing spent
(progression.BaseStatAtLevel); every point above that comes back as a free
stat point, so the total is kept and nothing earned is lost, including points
kept through a level-down. A lower maximum trims the stored current HP/MP as
any other maximum drop does; entry publishes the result on reconnect.
================
*/
func (rt *Runtime) OperatorResetStats(division, name string) error {
	unlock := rt.lockDivision(division)
	defer unlock()
	c := rt.findCharacter(division, name)
	if c == nil {
		return fmt.Errorf("character not found")
	}
	if !rt.deps.Update(c, "operator-reset-stats", func() bool {
		if c.DeletePending {
			return false
		}
		// The recall scroll's rule (progression.ResetStats, #1609): it validates
		// the raw fields, so a refused record is never invented or truncated.
		reset, ok := progression.ResetStats(c)
		if !ok {
			return false
		}
		next := c.Snapshot()
		strength, intellect, free := reset.Strength, reset.Intellect, reset.Points
		next.Strength, next.Intellect, next.StatPoints = &strength, &intellect, &free
		if err := rt.operatorResetVitals(next); err != nil {
			return false
		}
		c.Strength, c.Intellect, c.StatPoints = next.Strength, next.Intellect, next.StatPoints
		c.CurrentHP, c.CurrentMP = next.CurrentHP, next.CurrentMP
		return true
	}) {
		return fmt.Errorf("character stat reset refused")
	}
	return nil
}

/*
================
OperatorRescue

The caller must hold the character's transport control lease. Session teardown
retires combat, trade, movement and companion runtime through their owners.
The next login rebuilds companions at the rescued position, including mounts.
================
*/
func (rt *Runtime) OperatorRescue(division, name string, townID uint32) error {
	unlock := rt.lockDivision(division)
	defer unlock()
	var destination simulation.Spawn
	found := false
	for _, town := range rt.OperatorTowns() {
		if town.ID == townID {
			destination, found = town.Position, true
			break
		}
	}
	if !found {
		return fmt.Errorf("unknown rescue town")
	}
	c := rt.findCharacter(division, name)
	if c == nil {
		return fmt.Errorf("character not found")
	}
	if rt.characterSnapshot(division, c).DeletePending {
		return fmt.Errorf("character is pending deletion")
	}
	// Retire the same owners even for an offline character with stale runtime.
	rt.forgetCharacterLocked(division, c.Name)
	if !rt.deps.Update(c, "operator-rescue", func() bool {
		if c.DeletePending {
			return false
		}
		world := simulation.SeedWorldState(c)
		world.Spawn = destination
		world.MoveSegment = nil
		world.Sitting = false
		world.PostureTransitionUntilMs = 0
		world.SpawnSet = true
		world.MovementSourceSeeded = true
		writeBackWorld(c, world)
		rt.notePlacement(c, "operator-rescue")
		c.World.MoveSegment = nil
		c.World.AuthoredAreaReturn = nil
		c.World.DungeonFloorIndex = nil
		c.World.PackedInstance = nil
		c.World.SavedReturn = &domain.SavedReturnLocation{Definition: 1, RegionID: destination.RegionID,
			X: float32(destination.X), Y: float32(destination.Y), Z: float32(destination.Z)}
		c.NativeTeleportMode = 0
		return true
	}) {
		return fmt.Errorf("character rescue refused")
	}
	return nil
}
