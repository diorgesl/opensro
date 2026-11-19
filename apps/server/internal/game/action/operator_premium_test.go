/*
===========================================================================

operator_premium_test.go - the operator's premium clear

===========================================================================
*/
package action

import (
	"reflect"
	"testing"

	"opensro.online/server/internal/domain"
	"opensro.online/server/internal/game/enterworld"
)

/*
================
TestOperatorClearPremiumKeepsScrollsAndBooth
================
*/
func TestOperatorClearPremiumKeepsScrollsAndBooth(t *testing.T) {
	c := testCharacter()
	scroll := domain.ParamJob{ItemRefObjID: 7, Codename: "ITEM_EXP_SCROLL", Param: paramExpRate, Value: 50, EndUnixMs: 1 << 50}
	booth := domain.CompositeJob{Kind: domain.CompositeBuffItem, PackageRefObjID: 9, Target: 11, EndUnixMs: 1 << 50}
	c.ParamJobs = []domain.ParamJob{
		{ItemRefObjID: 5, Codename: "ITEM_PREMIUM", Param: paramPremiumExpRate, Value: 100, EndUnixMs: 1 << 50},
		scroll,
		{ItemRefObjID: 5, Codename: "ITEM_PREMIUM", Param: paramPremiumSkillExpRate, Value: 100, EndUnixMs: 1 << 50},
		{ItemRefObjID: 5, Codename: "ITEM_PREMIUM", Param: paramDeathExpKept, Value: 10, EndUnixMs: 1 << 50},
	}
	c.PremiumClock = newPremiumClock(5, 0, 1<<50, 10_800_000)
	c.CompositeJobs = []domain.CompositeJob{
		{Kind: domain.CompositeUsedItemLimit, PackageRefObjID: 9, Target: 12, Uses: 3, MaxUses: 3, EndUnixMs: 1 << 50},
		booth,
		{Kind: domain.CompositeUsedQuestLimit, PackageRefObjID: 9, QuestCodename: "QNO_PREMIUM", EndUnixMs: 1 << 50},
	}
	luck := domain.TimedSkillJob{SkillID: 900, Token: 2, RemainingMs: 60_000}
	c.TimedSkillJobs = []domain.TimedSkillJob{{SkillID: 31120, Token: 1, RemainingMs: 60_000}, luck}
	rt, _ := newTestRuntime(c, testItems())
	rt.deps.(*enterworld.Deps).Skills = staticSkillSource{
		31120: {ID: 31120, Codename: "SKILL_MALL_PRE_APRU_GLOBAL_4W_PLUS_01"},
		900:   {ID: 900, Codename: "SKILL_MALL_APRU_LUCK_01"},
	}
	if err := rt.OperatorClearPremium(testDivision, c.Name); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.TimedSkillJobs, []domain.TimedSkillJob{luck}) {
		t.Fatalf("premium package buff survived or another buff went: %+v", c.TimedSkillJobs)
	}
	if c.PremiumClock != nil || premiumRunning(c, 0) {
		t.Fatalf("premium survived: %+v / %+v", c.PremiumClock, c.ParamJobs)
	}
	if !reflect.DeepEqual(c.ParamJobs, []domain.ParamJob{scroll}) || !reflect.DeepEqual(c.CompositeJobs, []domain.CompositeJob{booth}) {
		t.Fatalf("wrong jobs kept: %+v / %+v", c.ParamJobs, c.CompositeJobs)
	}
	before := c.Snapshot()
	if err := rt.OperatorClearPremium(testDivision, c.Name); err != nil || !reflect.DeepEqual(before, c.Snapshot()) {
		t.Fatal("repeated clear changed the character", err)
	}
}

/*
================
TestOperatorClearPremiumRefusesMissingAndDeleted
================
*/
func TestOperatorClearPremiumRefusesMissingAndDeleted(t *testing.T) {
	c := testCharacter()
	c.DeletePending = true
	c.PremiumClock = newPremiumClock(5, 0, 1<<50, 10_800_000)
	rt, _ := newTestRuntime(c, testItems())
	before := c.Snapshot()
	for _, name := range []string{"missing", c.Name} {
		if err := rt.OperatorClearPremium(testDivision, name); err == nil {
			t.Fatal("invalid character accepted")
		}
		if !reflect.DeepEqual(before, c.Snapshot()) {
			t.Fatal("refusal mutated character")
		}
	}
}

/*
================
TestOperatorClearPremiumPersistsThroughAuthorityRestart
================
*/
func TestOperatorClearPremiumPersistsThroughAuthorityRestart(t *testing.T) {
	seed := testCharacter()
	seed.PremiumClock = newPremiumClock(5, 0, 1<<50, 10_800_000)
	seed.ParamJobs = []domain.ParamJob{{ItemRefObjID: 5, Param: paramPremiumExpRate, Value: 100, EndUnixMs: 1 << 50}}
	d := openDoorRuntime(t, t.TempDir(), seed)
	deps := d.rt.deps.(*enterworld.Deps)
	deps.UpdateCharacter = d.authority.UpdateCharacter
	deps.ReadCharacter = func(_ string, read func()) { d.authority.ReadState(read) }
	if err := d.rt.OperatorClearPremium(testDivision, d.character.Name); err != nil {
		t.Fatal(err)
	}
	if health := d.authority.Health(); health.LastError != "" {
		t.Fatalf("storage: %+v", health)
	}
	if got := d.reboot(t).character; got.PremiumClock != nil || len(got.ParamJobs) != 0 {
		t.Fatalf("premium came back after restart: %+v / %+v", got.PremiumClock, got.ParamJobs)
	}
}
