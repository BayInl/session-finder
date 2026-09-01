package decisions

import (
	"testing"

	"github.com/BayInl/session-finder/internal/record"
)

func TestPrecisionGateRegexes(t *testing.T) {
	tests := []struct {
		name     string
		matches  func(string) bool
		positive []string
		negative []string
	}{
		{
			name:     "future plan",
			matches:  segmentFuturePlanRE.MatchString,
			positive: []string{"Going to choose SQLite because it is local.", "接下来改用 SQLite，因为它适合本地。"},
			negative: []string{"We chose SQLite because it is local.", "我们选择 SQLite，因为它适合本地。"},
		},
		{
			name:     "table",
			matches:  segmentTableRE.MatchString,
			positive: []string{"| Choice | Reason |", "| 方案 | 理由 |"},
			negative: []string{"Choose SQLite because it is local.", "选择 SQLite，因为它适合本地。"},
		},
		{
			name:     "heading",
			matches:  segmentHeadingRE.MatchString,
			positive: []string{"**Recommended choice**", "__推荐方案__"},
			negative: []string{"**Choose SQLite** because it is local.", "__选择 SQLite__，因为它适合本地。"},
		},
		{
			name:     "bullet",
			matches:  segmentBulletRE.MatchString,
			positive: []string{"- Use SQLite because it is local.", "1. 选择 SQLite，因为它适合本地。"},
			negative: []string{"Use SQLite because it is local.", "选择 SQLite，因为它适合本地。"},
		},
		{
			name:     "explicit choice",
			matches:  segmentExplicitChoiceRE.MatchString,
			positive: []string{"We chose SQLite because it is local.", "优先使用 SQLite，因为它适合本地。"},
			negative: []string{"SQLite keeps the cache local.", "SQLite 适合本地缓存。"},
		},
		{
			name:     "progress noise",
			matches:  progressNoiseRE.MatchString,
			positive: []string{"I'm checking which database to recommend because the environment matters.", "I am comparing SQLite before I choose because portability matters.", "我正在检查应该推荐哪个数据库。"},
			negative: []string{"We chose SQLite because it is local.", "我们选择 SQLite，因为它适合本地。"},
		},
		{
			name:     "consequence before choice",
			matches:  consequenceBeforeChoiceRE.MatchString,
			positive: []string{"Shared writes are required, so choose PostgreSQL.", "Latency matters; therefore adopt Redis.", "部署要求共享写入，所以选择PostgreSQL。", "本地优先；因此采用SQLite。"},
			negative: []string{"Choose SQLite because it is local.", "所以需要继续比较方案。"},
		},
		{
			name:     "consequence after choice",
			matches:  consequenceAfterRE.MatchString,
			positive: []string{"Use SQLite, so deployment stays local.", "选择 SQLite，这样部署更简单。"},
			negative: []string{"Use SQLite because it is local.", "选择 SQLite，因为部署更简单。"},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			for _, text := range testCase.positive {
				if !testCase.matches(text) {
					t.Errorf("positive did not match: %q", text)
				}
			}
			for _, text := range testCase.negative {
				if testCase.matches(text) {
					t.Errorf("negative matched: %q", text)
				}
			}
		})
	}
}

func TestPrecisionGatesFilterStructuralAndNarrativeNoise(t *testing.T) {
	for _, text := range []string{
		"Going to choose SQLite because it is local.",
		"接下来改用 SQLite，因为它适合本地。",
		"| Recommend SQLite | because it is local |",
		"| 推荐 SQLite | 因为它适合本地 |",
		"**Choose SQLite because it is local**",
		"__选择 SQLite，因为它适合本地__",
		"- SQLite is the local cache because it is portable.",
		"1. SQLite 是本地缓存，因为它便于携带。",
		"I'm checking which database to recommend because the environment matters.",
		"I am comparing SQLite before I choose because portability matters.",
	} {
		if candidates := Scan([]record.MessageRecord{msg("s1", "assistant", text)}); len(candidates) != 0 {
			t.Errorf("noise candidate for %q = %#v", text, candidates)
		}
	}
}

func TestPrecisionGatesKeepExplicitBulletChoices(t *testing.T) {
	for _, text := range []string{
		"- Choose SQLite because it is local.",
		"1. 选择 SQLite，因为它适合本地。",
	} {
		candidates := Scan([]record.MessageRecord{msg("s1", "user", text)})
		if len(candidates) != 1 || candidates[0].Chosen != "SQLite" {
			t.Errorf("explicit bullet %q = %#v", text, candidates)
		}
	}
}

func TestConsequenceGatesExtractResolvedDecision(t *testing.T) {
	for _, testCase := range []struct {
		text      string
		chosen    string
		rationale string
	}{
		{"共享部署要求并发，所以选择PostgreSQL。", "PostgreSQL", "共享部署要求并发"},
		{"本地缓存必须零配置；因此采用SQLite。", "SQLite", "本地缓存必须零配置"},
		{"Use SQLite, so deployment stays local.", "SQLite", "deployment stays local"},
		{"选择 SQLite，这样部署更简单。", "SQLite", "部署更简单"},
	} {
		candidates := Scan([]record.MessageRecord{msg("s1", "user", testCase.text)})
		if len(candidates) != 1 || candidates[0].Chosen != testCase.chosen || candidates[0].Rationale != testCase.rationale {
			t.Errorf("consequence %q = %#v", testCase.text, candidates)
		}
	}
}
