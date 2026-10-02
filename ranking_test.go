package scholarship

import (
	"math/big"
	"sort"
	"testing"
)

// refModel 是测试用的串行参考实现，与库实现相互独立。
type refModel struct {
	items   []string
	weights map[string]int
	scores  map[string]map[string]map[string]int
	k       int
}

func newRefModel(items []string, weights map[string]int, k int) *refModel {
	return &refModel{
		items:   items,
		weights: weights,
		scores:  make(map[string]map[string]map[string]int),
		k:       k,
	}
}

func (m *refModel) put(app, reviewer string, changes map[string]int) {
	revs, ok := m.scores[app]
	if !ok {
		revs = make(map[string]map[string]int)
		m.scores[app] = revs
	}
	s, ok := revs[reviewer]
	if !ok {
		s = make(map[string]int)
		revs[reviewer] = s
	}
	for it, v := range changes {
		s[it] = v
	}
}

func (m *refModel) preview() []Award {
	wsum := 0
	for _, it := range m.items {
		wsum += m.weights[it]
	}
	var out []Award
	for app, revs := range m.scores {
		total := new(big.Rat)
		complete := 0
		for _, s := range revs {
			if len(s) != len(m.items) {
				continue
			}
			w := new(big.Rat)
			ok := true
			for _, it := range m.items {
				v, has := s[it]
				if !has {
					ok = false
					break
				}
				w.Add(w, new(big.Rat).SetInt64(int64(v*m.weights[it])))
			}
			if !ok {
				continue
			}
			w.Quo(w, new(big.Rat).SetInt64(int64(wsum)))
			total.Add(total, w)
			complete++
		}
		if complete == 0 {
			continue
		}
		score := new(big.Rat).Quo(total, new(big.Rat).SetInt64(int64(complete)))
		out = append(out, Award{ApplicationID: app, Score: score, CompleteReviewers: complete})
	}
	sort.Slice(out, func(i, j int) bool {
		if c := out[i].Score.Cmp(out[j].Score); c != 0 {
			return c > 0
		}
		return out[i].ApplicationID < out[j].ApplicationID
	})
	if len(out) > m.k {
		out = out[:m.k]
	}
	return out
}

func assertAwardsEqual(t *testing.T, got, want []Award) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("award count: got %d (%v), want %d (%v)", len(got), got, len(want), want)
	}
	for i := range got {
		if got[i].ApplicationID != want[i].ApplicationID {
			t.Fatalf("award %d: got app %q, want %q", i, got[i].ApplicationID, want[i].ApplicationID)
		}
		if got[i].Score.Cmp(want[i].Score) != 0 {
			t.Fatalf("award %d (%s): got score %s, want %s", i, got[i].ApplicationID, got[i].Score, want[i].Score)
		}
		if got[i].CompleteReviewers != want[i].CompleteReviewers {
			t.Fatalf("award %d (%s): got %d complete reviewers, want %d",
				i, got[i].ApplicationID, got[i].CompleteReviewers, want[i].CompleteReviewers)
		}
	}
}

// 残缺评审不参与计算，补齐后参与；重复提交只保留当前分值且不增加评审人数。
func TestIncompleteAndDuplicateSubmissions(t *testing.T) {
	items := []string{"a", "b", "c"}
	weights := map[string]int{"a": 1, "b": 2, "c": 3}
	apps := map[string][]string{
		"app-1": {"r1", "r2"},
		"app-2": {"r3"},
	}
	b := New(apps, items, weights, 2)
	ref := newRefModel(items, weights, 2)

	put := func(app, rev string, changes map[string]int) {
		b.PutScores(app, rev, changes)
		ref.put(app, rev, changes)
	}

	// r1 只提交两项，属于残缺评审。
	put("app-1", "r1", map[string]int{"a": 10, "b": 10})
	if got := b.PreviewAwards(); len(got) != 0 {
		t.Fatalf("incomplete reviewer must not count, got %v", got)
	}

	// r1 补齐第三项。
	put("app-1", "r1", map[string]int{"c": 4})
	assertAwardsEqual(t, b.PreviewAwards(), ref.preview())

	// r2 完整提交；app-2 的 r3 完整提交。
	put("app-1", "r2", map[string]int{"a": 8, "b": 7, "c": 9})
	put("app-2", "r3", map[string]int{"a": 6, "b": 6, "c": 6})
	assertAwardsEqual(t, b.PreviewAwards(), ref.preview())

	// 重复提交：覆盖旧值，评审人数不变。
	put("app-1", "r1", map[string]int{"a": 1})
	put("app-1", "r1", map[string]int{"a": 2, "b": 3})
	got := b.PreviewAwards()
	assertAwardsEqual(t, got, ref.preview())
	for _, aw := range got {
		if aw.ApplicationID == "app-1" && aw.CompleteReviewers != 2 {
			t.Fatalf("duplicate submission changed reviewer count: %d", aw.CompleteReviewers)
		}
	}
}

// 四舍五入到整数后相同、实际分数不同，且差异决定最后一个名额。
func TestExactRationalDecidesLastSlot(t *testing.T) {
	items := []string{"a", "b", "c"}
	weights := map[string]int{"a": 1, "b": 1, "c": 1}
	apps := map[string][]string{
		"app-a": {"r1"},
		"app-b": {"r1", "r2", "r3"},
	}
	b := New(apps, items, weights, 1)

	// app-a: 29/3 = 9.666...
	b.PutScores("app-a", "r1", map[string]int{"a": 10, "b": 10, "c": 9})
	// app-b: (29/3 + 29/3 + 10)/3 = 88/9 = 9.777...
	b.PutScores("app-b", "r1", map[string]int{"a": 10, "b": 10, "c": 9})
	b.PutScores("app-b", "r2", map[string]int{"a": 10, "b": 10, "c": 9})
	b.PutScores("app-b", "r3", map[string]int{"a": 10, "b": 10, "c": 10})

	got := b.PreviewAwards()
	if len(got) != 1 {
		t.Fatalf("want exactly 1 award, got %v", got)
	}
	// 两者四舍五入到整数都是 10，但 88/9 > 29/3，必须选 app-b。
	if got[0].ApplicationID != "app-b" {
		t.Fatalf("exact comparison broken: got %s, want app-b", got[0].ApplicationID)
	}
	want := big.NewRat(88, 9)
	if got[0].Score.Cmp(want) != 0 {
		t.Fatalf("score: got %s, want %s", got[0].Score, want)
	}
	if got[0].CompleteReviewers != 3 {
		t.Fatalf("complete reviewers: got %d, want 3", got[0].CompleteReviewers)
	}
}

// 同分按申请 ID 字典序；权重替换只影响之后的预览；K 截断。
func TestTieBreakWeightsAndLimit(t *testing.T) {
	items := []string{"x", "y"}
	weights := map[string]int{"x": 1, "y": 1}
	apps := map[string][]string{
		"app-b": {"r1"},
		"app-a": {"r2"},
		"app-c": {"r3"},
	}
	b := New(apps, items, weights, 2)
	b.PutScores("app-a", "r2", map[string]int{"x": 8, "y": 8})
	b.PutScores("app-b", "r1", map[string]int{"x": 8, "y": 8})
	b.PutScores("app-c", "r3", map[string]int{"x": 1, "y": 1})

	got := b.PreviewAwards()
	if len(got) != 2 || got[0].ApplicationID != "app-a" || got[1].ApplicationID != "app-b" {
		t.Fatalf("tie-break/limit wrong: %v", got)
	}

	// 改权重后 app-c 反超。
	b.SetWeights(map[string]int{"x": 100, "y": 1})
	b.PutScores("app-c", "r3", map[string]int{"x": 10, "y": 1})
	got = b.PreviewAwards()
	if got[0].ApplicationID != "app-c" {
		t.Fatalf("new weights not applied: %v", got)
	}
	// 权重不修改原始评分：改回等权后恢复平局顺序。
	b.SetWeights(map[string]int{"x": 1, "y": 1})
	got = b.PreviewAwards()
	if got[0].ApplicationID != "app-a" || got[1].ApplicationID != "app-b" {
		t.Fatalf("weights mutated raw scores: %v", got)
	}
}
