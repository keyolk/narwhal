// settings.go registers the judge's settings with internal/config: whether
// each judgement is consulted, and the threshold it acts at.
//
// The thresholds' defaults are the calibrated constants beside each
// judgement, which say what they were measured on. An operator who moves
// one moves it against that measurement; `narwhal config` shows the
// default next to the value.
package broker

import (
	"time"

	"github.com/keyolk/narwhal/internal/config"
)

// judgement is one Jev question the broker puts, as the operator controls
// it.
type judgement struct {
	name      string
	on        config.Key
	threshold config.Key
}

func judgementSetting(name string, threshold float64, help string) judgement {
	return judgement{
		name: name,
		on: config.Register(config.Key{Name: "jev." + name + ".enabled", Kind: config.Bool, Default: "true",
			Help: "consult the judge on: " + help}),
		threshold: config.Register(config.Key{Name: "jev." + name + ".threshold", Kind: config.Float,
			Default: ftoa(threshold), Help: "probability at which it acts: " + help}),
	}
}

func ftoa(f float64) string {
	return config.FormatFloat(f)
}

var (
	settingJev = config.Register(config.Key{Name: "jev.enabled", Kind: config.Bool, Default: "true",
		Help: "consult the Jev judge at all (every jev.* judgement)"})
	settingJudgeTimeout = config.Register(config.Key{Name: "jev.timeout", Kind: config.Duration,
		Default: judgeTimeout.String(), Help: "how long one dispatcher judgement may take"})

	judgements = map[string]judgement{
		QuestionNoResult:   judgementSetting(QuestionNoResult, NoResultThreshold, "a worker that exited without task-done left no result (retry at or above)"),
		QuestionSameCause:  judgementSetting(QuestionSameCause, SameCauseThreshold, "two failures share a cause (move a tier up at or above)"),
		QuestionCriterion:  judgementSetting(QuestionCriterion, CriterionThreshold, "an outcome meets its criterion (hand back below)"),
		QuestionAnswered:   judgementSetting(QuestionAnswered, AnsweredThreshold, "an outcome answers its task (hand back below)"),
		QuestionGaveUp:     judgementSetting(QuestionGaveUp, GaveUpThreshold, "a negative outcome was reached without trying another way (hand back above)"),
		QuestionAffected:   judgementSetting(QuestionAffected, AffectedThreshold, "an upstream change reaches a dependent (leave it finished below)"),
		QuestionFolded:     judgementSetting(QuestionFolded, FoldedThreshold, "the synthesis folds a peer in (hand back below)"),
		QuestionStuck:      judgementSetting(QuestionStuck, StuckThreshold, "a running worker is stuck (flag at or above)"),
		QuestionRoute:      judgementSetting(QuestionRoute, RouteThreshold, "an operator instruction is for a worker (relay at or above)"),
		QuestionOverturned: judgementSetting(QuestionOverturned, OverturnedThreshold, "an operator's answer overturns what was assumed (reopen at or above)"),
		QuestionFollowed:   judgementSetting(QuestionFollowed, FollowedThreshold, "an outcome follows the operator's answers (hand back below)"),
	}
)

// judging reports whether the named judgement is to be put: the judge is
// installed and neither it nor this judgement is switched off.
func (r *Run) judging(name string) bool {
	if r.currentJudge() == nil || !config.GetBool(settingJev.Name) {
		return false
	}
	j, ok := judgements[name]
	return !ok || config.GetBool(j.on.Name)
}

// threshold is the named judgement's threshold, as configured.
func threshold(name string) float64 {
	return config.GetFloat(judgements[name].threshold.Name)
}

func currentJudgeTimeout() time.Duration {
	return config.GetDuration(settingJudgeTimeout.Name)
}
