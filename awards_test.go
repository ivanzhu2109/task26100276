package scholarship

import (
	"math/big"
	"sort"
	"testing"
)

// refState 是测试内的串行参考实现，与被测库独立。
type refState struct {
	items     []string
	k         int
	weights   map[string]int
	reviewers map[string][]string
	scores    map[string]map[string]map[string]int
}

func newRefState(appIDs []string, reviewers map[string][]string, items []string, weights map[string]int, k int) *refState {
	r := &refState{
		items:     append([]string(nil), items...),
		k:         k,
		weights:   make(map[string]int, len(weights)),
		reviewers: make(map[string][]string, len(appIDs)),
		scores:    make(map[string]map[string]map[string]int, len(appIDs)),
	}
	for k2, v := range weights {
		r.weights[k2] = v
	}
	for _, id := range appIDs {
		r.reviewers[id] = append([]string(nil), reviewers[id]...)
		r.scores[id] = make(map[string]map[string]int)
	}
	return r
}

func (r *refState) put(appID, reviewer string, changes map[string]int) {
	cur := r.scores[appID][reviewer]
	if cur == nil {
		cur = make(map[string]int)
		r.scores[appID][reviewer] = cur
	}
	for item, v := range changes {
		cur[item] = v
	}
}

func (r *refState) setWeights(w map[string]int) {
	r.weights = make(map[string]int, len(w))
	for k, v := range w {
		r.weights[k] = v
	}
}

func (r *refState) preview() []Award {
	weightSum := 0
	for _, it := range r.items {
		weightSum += r.weights[it]
	}
	var out []Award
	for appID, byReviewer := range r.scores {
		total := new(big.Rat)
		complete := 0
		for _, items := range byReviewer {
			if len(items) != len(r.items) {
				continue
			}
			sum := new(big.Rat)
			for _, it := range r.items {
				sum.Add(sum, new(big.Rat).SetInt64(int64(r.weights[it]*items[it])))
			}
			sum.Quo(sum, new(big.Rat).SetInt64(int64(weightSum)))
			total.Add(total, sum)
			complete++
		}
		if complete == 0 {
			continue
		}
		out = append(out, Award{
			ApplicationID:     appID,
			Score:             new(big.Rat).Quo(total, new(big.Rat).SetInt64(int64(complete))),
			CompleteReviewers: complete,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if c := out[i].Score.Cmp(out[j].Score); c != 0 {
			return c > 0
		}
		return out[i].ApplicationID < out[j].ApplicationID
	})
	if len(out) > r.k {
		out = out[:r.k]
	}
	if out == nil {
		out = []Award{}
	}
	return out
}

func assertAwardsEqual(t *testing.T, got, want []Award) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("获奖人数不符: got %d (%v), want %d (%v)", len(got), got, len(want), want)
	}
	for i := range got {
		if got[i].ApplicationID != want[i].ApplicationID {
			t.Fatalf("第 %d 名申请不符: got %q, want %q", i, got[i].ApplicationID, want[i].ApplicationID)
		}
		if got[i].Score.Cmp(want[i].Score) != 0 {
			t.Fatalf("第 %d 名(%q)分数不符: got %s, want %s", i, got[i].ApplicationID, got[i].Score, want[i].Score)
		}
		if got[i].CompleteReviewers != want[i].CompleteReviewers {
			t.Fatalf("第 %d 名(%q)完整评审人数不符: got %d, want %d", i, got[i].ApplicationID, got[i].CompleteReviewers, want[i].CompleteReviewers)
		}
	}
}

func TestIncompleteThenCompleteAndDuplicate(t *testing.T) {
	items := []string{"a", "b", "c"}
	weights := map[string]int{"a": 1, "b": 2, "c": 3}
	reviewers := map[string][]string{
		"app1": {"r1", "r2"},
		"app2": {"r1"},
	}
	b, err := NewBoard([]string{"app1", "app2"}, reviewers, items, weights, 2)
	if err != nil {
		t.Fatal(err)
	}
	ref := newRefState([]string{"app1", "app2"}, reviewers, items, weights, 2)

	step := func(appID, reviewer string, changes map[string]int) {
		t.Helper()
		if err := b.PutScores(appID, reviewer, changes); err != nil {
			t.Fatalf("PutScores(%s,%s): %v", appID, reviewer, err)
		}
		ref.put(appID, reviewer, changes)
		assertAwardsEqual(t, b.PreviewAwards(), ref.preview())
	}

	// 残缺评审：不参与计算，预览为空。
	step("app1", "r1", map[string]int{"a": 5, "b": 5})
	if got := b.PreviewAwards(); len(got) != 0 {
		t.Fatalf("残缺评审不应产生获奖名单: %v", got)
	}

	// 补齐后参与计算。
	step("app1", "r1", map[string]int{"c": 5})
	if got := b.PreviewAwards(); len(got) != 1 || got[0].CompleteReviewers != 1 {
		t.Fatalf("补齐后应有 1 名获奖者、1 名完整评审: %v", got)
	}

	// 重复提交同一项只保留当前分值，不增加评审人数。
	step("app1", "r1", map[string]int{"a": 9})
	step("app1", "r1", map[string]int{"a": 3})
	got := b.PreviewAwards()
	if len(got) != 1 || got[0].CompleteReviewers != 1 {
		t.Fatalf("重复提交不得增加评审人数: %v", got)
	}
	wantScore := new(big.Rat).SetFrac64(3*1+5*2+5*3, 6) // a=3,b=5,c=5 加权平均
	if got[0].Score.Cmp(wantScore) != 0 {
		t.Fatalf("重复提交后分数应为 %s, got %s", wantScore, got[0].Score)
	}

	// 第二名评审补齐，等权平均。
	step("app1", "r2", map[string]int{"a": 10, "b": 10, "c": 10})
	step("app2", "r1", map[string]int{"a": 1, "b": 1, "c": 1})
	got = b.PreviewAwards()
	if len(got) != 2 || got[0].ApplicationID != "app1" || got[0].CompleteReviewers != 2 {
		t.Fatalf("两名完整评审后的名单不符: %v", got)
	}
}

func TestTieBreakByApplicationID(t *testing.T) {
	items := []string{"x"}
	weights := map[string]int{"x": 1}
	reviewers := map[string][]string{"b-app": {"r"}, "a-app": {"r"}, "c-app": {"r"}}
	b, err := NewBoard([]string{"b-app", "a-app", "c-app"}, reviewers, items, weights, 3)
	if err != nil {
		t.Fatal(err)
	}
	for _, app := range []string{"b-app", "a-app", "c-app"} {
		if err := b.PutScores(app, "r", map[string]int{"x": 7}); err != nil {
			t.Fatal(err)
		}
	}
	got := b.PreviewAwards()
	want := []string{"a-app", "b-app", "c-app"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i, id := range want {
		if got[i].ApplicationID != id {
			t.Fatalf("同分应按申请 ID 字典序: got %v, want %v", got, want)
		}
	}
}

func TestSetWeightsAffectsLaterPreviews(t *testing.T) {
	items := []string{"p", "q"}
	weights := map[string]int{"p": 1, "q": 1}
	reviewers := map[string][]string{"app1": {"r"}, "app2": {"r"}}
	b, err := NewBoard([]string{"app1", "app2"}, reviewers, items, weights, 1)
	if err != nil {
		t.Fatal(err)
	}
	mustPut := func(appID string, changes map[string]int) {
		t.Helper()
		if err := b.PutScores(appID, "r", changes); err != nil {
			t.Fatal(err)
		}
	}
	mustPut("app1", map[string]int{"p": 10, "q": 1})
	mustPut("app2", map[string]int{"p": 1, "q": 10})

	if got := b.PreviewAwards(); len(got) != 1 || got[0].ApplicationID != "app1" {
		t.Fatalf("等权同分应按 ID 字典序取 app1: %v", got)
	}
	if err := b.SetWeights(map[string]int{"p": 1, "q": 100}); err != nil {
		t.Fatal(err)
	}
	got := b.PreviewAwards()
	if len(got) != 1 || got[0].ApplicationID != "app2" {
		t.Fatalf("权重替换后 app2 应胜出: %v", got)
	}
	want := new(big.Rat).SetFrac64(1*1+10*100, 101)
	if got[0].Score.Cmp(want) != 0 {
		t.Fatalf("权重替换后分数应为 %s, got %s", want, got[0].Score)
	}
}

// TestExactScoresNotRounded 构造两个四舍五入到一位小数相同、
// 但精确分数不同的申请，验证最后一个名额按精确分数决定。
func TestExactScoresNotRounded(t *testing.T) {
	items := []string{"p", "q"}
	weights := map[string]int{"p": 1, "q": 3} // 权重和 4
	reviewers := map[string][]string{
		"z-app": {"r1", "r2", "r3"},
		"a-app": {"r1", "r2", "r3", "r4", "r5"},
	}
	b, err := NewBoard([]string{"z-app", "a-app"}, reviewers, items, weights, 1)
	if err != nil {
		t.Fatal(err)
	}
	mustPut := func(appID, reviewer string, p, q int) {
		t.Helper()
		if err := b.PutScores(appID, reviewer, map[string]int{"p": p, "q": q}); err != nil {
			t.Fatal(err)
		}
	}
	// z-app: (10,10),(10,10),(6,10) -> 加权总分 116，3 名评审 -> 29/3 ≈ 9.6667
	mustPut("z-app", "r1", 10, 10)
	mustPut("z-app", "r2", 10, 10)
	mustPut("z-app", "r3", 6, 10)
	// a-app: 四名 (10,10) 加一名 (3,10) -> 加权总分 193，5 名评审 -> 193/20 = 9.65
	mustPut("a-app", "r1", 10, 10)
	mustPut("a-app", "r2", 10, 10)
	mustPut("a-app", "r3", 10, 10)
	mustPut("a-app", "r4", 10, 10)
	mustPut("a-app", "r5", 3, 10)

	got := b.PreviewAwards()
	if len(got) != 1 {
		t.Fatalf("k=1 应只有 1 名获奖者: %v", got)
	}
	// 两者四舍五入到一位小数都是 9.7，若先四舍五入则同分，
	// 按 ID 字典序会错选 a-app；精确比较应选 z-app。
	if got[0].ApplicationID != "z-app" {
		t.Fatalf("精确分数 29/3 > 193/20，唯一名额应归 z-app: %v", got)
	}
	want := new(big.Rat).SetFrac64(29, 3)
	if got[0].Score.Cmp(want) != 0 {
		t.Fatalf("z-app 分数应为 %s, got %s", want, got[0].Score)
	}
	// 确认两者确实只在精确层面不同。
	zScore := new(big.Rat).SetFrac64(29, 3)
	aScore := new(big.Rat).SetFrac64(193, 20)
	if zScore.Cmp(aScore) <= 0 {
		t.Fatalf("测试构造有误: %s 应大于 %s", zScore, aScore)
	}
	if zScore.FloatString(1) != aScore.FloatString(1) {
		t.Fatalf("测试构造有误: 两者一位小数应相同, got %s vs %s", zScore.FloatString(1), aScore.FloatString(1))
	}
}

func TestValidationErrors(t *testing.T) {
	items := []string{"a", "b"}
	weights := map[string]int{"a": 1, "b": 1}
	reviewers := map[string][]string{"app1": {"r1"}}

	if _, err := NewBoard([]string{"app1"}, reviewers, nil, weights, 1); err == nil {
		t.Fatal("空评分项应报错")
	}
	if _, err := NewBoard([]string{"app1"}, reviewers, []string{"a", "b", "c", "d", "e", "f"}, weights, 1); err == nil {
		t.Fatal("超过 5 个评分项应报错")
	}
	if _, err := NewBoard([]string{"app1"}, reviewers, items, map[string]int{"a": 0, "b": 1}, 1); err == nil {
		t.Fatal("权重为 0 应报错")
	}
	if _, err := NewBoard([]string{"app1"}, reviewers, items, map[string]int{"a": 1, "b": 101}, 1); err == nil {
		t.Fatal("权重超过 100 应报错")
	}
	if _, err := NewBoard([]string{"app1"}, reviewers, items, map[string]int{"a": 1}, 1); err == nil {
		t.Fatal("权重缺项应报错")
	}
	if _, err := NewBoard([]string{"app1"}, reviewers, items, weights, 0); err == nil {
		t.Fatal("k 为 0 应报错")
	}
	if _, err := NewBoard([]string{"app1"}, map[string][]string{"ghost": {"r1"}}, items, weights, 1); err == nil {
		t.Fatal("未知申请的评审分配应报错")
	}

	b, err := NewBoard([]string{"app1"}, reviewers, items, weights, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.PutScores("ghost", "r1", map[string]int{"a": 5}); err == nil {
		t.Fatal("未知申请应报错")
	}
	if err := b.PutScores("app1", "ghost", map[string]int{"a": 5}); err == nil {
		t.Fatal("未分配评审应报错")
	}
	if err := b.PutScores("app1", "r1", map[string]int{"a": 0}); err == nil {
		t.Fatal("分值 0 应报错")
	}
	if err := b.PutScores("app1", "r1", map[string]int{"a": 11}); err == nil {
		t.Fatal("分值 11 应报错")
	}
	if err := b.PutScores("app1", "r1", map[string]int{"nope": 5}); err == nil {
		t.Fatal("未知评分项应报错")
	}
	if err := b.PutScores("app1", "r1", map[string]int{}); err == nil {
		t.Fatal("空补丁应报错")
	}
	if err := b.SetWeights(map[string]int{"a": 1, "b": 0}); err == nil {
		t.Fatal("非法权重应报错")
	}
	// 失败的 SetWeights 不应改变原权重。
	if err := b.PutScores("app1", "r1", map[string]int{"a": 10, "b": 1}); err != nil {
		t.Fatal(err)
	}
	got := b.PreviewAwards()
	want := new(big.Rat).SetFrac64(11, 2) // (10*1+1*1)/2
	if len(got) != 1 || got[0].Score.Cmp(want) != 0 {
		t.Fatalf("失败的 SetWeights 后权重应保持不变: %v", got)
	}
}
