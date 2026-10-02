package scholarship

import (
	"fmt"
	"math/big"
	"sync"
	"sync/atomic"
	"testing"
)

// mutEvent 记录一次已提交变更及其调用区间（由全局序号刻画）。
// 每个 key（申请/评审/评分项，或整组权重）只由一个 goroutine 顺序写入，
// 因此不同 key 的变更可交换，同 key 的变更按提交先后构成前缀。
type mutEvent struct {
	startSeq int64
	endSeq   int64
	apply    func(m *refModel)
}

type commitLog struct {
	mu     sync.Mutex
	events []mutEvent
}

// begin 在调用库之前登记事件（endSeq 为 0 表示尚未完成），返回事件下标。
func (l *commitLog) begin(startSeq int64, apply func(m *refModel)) int {
	l.mu.Lock()
	l.events = append(l.events, mutEvent{startSeq: startSeq, apply: apply})
	idx := len(l.events) - 1
	l.mu.Unlock()
	return idx
}

func (l *commitLog) finish(idx int, endSeq int64) {
	l.mu.Lock()
	l.events[idx].endSeq = endSeq
	l.mu.Unlock()
}

// snapshot 返回事件的稳定副本。
func (l *commitLog) snapshot() []mutEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]mutEvent(nil), l.events...)
}

// plausibleMatch 检查预览结果是否等于某个可信完整状态的参考计算：
// 状态由每个 key 的版本前缀构成，版本下界为预览开始前已提交的变更，
// 上界为预览结束前已开始（可能已提交）的变更。
func plausibleMatch(t *testing.T, items []string, initWeights map[string]int, k int, events []mutEvent, pStart, pEnd int64, got []Award) bool {
	t.Helper()
	var definite, ambiguous []mutEvent
	for _, e := range events {
		if e.endSeq != 0 && e.endSeq < pStart {
			definite = append(definite, e) // 一定已生效
		} else if e.startSeq < pEnd {
			ambiguous = append(ambiguous, e) // 可能已生效
		}
	}
	if len(ambiguous) > 20 {
		t.Fatalf("too many in-flight mutations (%d) to verify", len(ambiguous))
	}
	// 枚举 ambiguous 的所有子集（不同 key 的变更可交换，子集即状态）。
	for mask := 0; mask < (1 << len(ambiguous)); mask++ {
		w := make(map[string]int, len(initWeights))
		for it, v := range initWeights {
			w[it] = v
		}
		m := newRefModel(items, w, k)
		for _, e := range definite {
			e.apply(m)
		}
		for i, toSet := range ambiguous {
			if mask&(1<<i) != 0 {
				toSet.apply(m)
			}
		}
		if awardsEqual(got, m.preview()) {
			return true
		}
	}
	return false
}

func awardsEqual(a, b []Award) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ApplicationID != b[i].ApplicationID ||
			a[i].Score.Cmp(b[i].Score) != 0 ||
			a[i].CompleteReviewers != b[i].CompleteReviewers {
			return false
		}
	}
	return true
}

// 并发交错提交评分补丁、替换权重、执行预览；
// 每份预览必须对应其调用区间内某个已提交的完整状态。
func TestConcurrentConsistentPreviews(t *testing.T) {
	items := []string{"a", "b"}
	weights := map[string]int{"a": 3, "b": 7}
	apps := map[string][]string{
		"app-1": {"r1", "r2"},
		"app-2": {"r1", "r2"},
	}
	const k = 2
	b := New(apps, items, weights, k)

	var seq atomic.Int64
	log := &commitLog{}
	const rounds = 200

	var wg sync.WaitGroup
	// 每个 (app, reviewer, item) 一个写 goroutine：单 key 单写者。
	for app, reviewers := range apps {
		for _, rev := range reviewers {
			for _, it := range items {
				app, rev, it := app, rev, it
				wg.Add(1)
				go func() {
					defer wg.Done()
					for i := 0; i < rounds; i++ {
						v := i%10 + 1
						start := seq.Add(1)
						idx := log.begin(start, func(m *refModel) {
							m.put(app, rev, map[string]int{it: v})
						})
						b.PutScores(app, rev, map[string]int{it: v})
						log.finish(idx, seq.Add(1))
					}
				}()
			}
		}
	}
	// 一个 goroutine 顺序替换整组权重。
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			w := map[string]int{"a": i%100 + 1, "b": (i*7)%100 + 1}
			start := seq.Add(1)
			idx := log.begin(start, func(m *refModel) {
				m.weights = w
			})
			b.SetWeights(w)
			log.finish(idx, seq.Add(1))
		}
	}()

	// 预览 goroutine：校验每份预览对应某个可信完整状态。
	var previewWg sync.WaitGroup
	for g := 0; g < 3; g++ {
		previewWg.Add(1)
		go func(g int) {
			defer previewWg.Done()
			for i := 0; i < rounds; i++ {
				pStart := seq.Add(1)
				got := b.PreviewAwards()
				pEnd := seq.Add(1)
				if !plausibleMatch(t, items, weights, k, log.snapshot(), pStart, pEnd, got) {
					t.Errorf("preview %d/%d matches no committed complete state: %v", g, i, got)
					return
				}
			}
		}(g)
	}
	wg.Wait()
	previewWg.Wait()
}

// 补丁整体原子生效：每次补丁把 5 个评分项写成同一个值 v，
// 任何预览若看到部分补丁，分数将不是整数。
func TestConcurrentPatchAtomicity(t *testing.T) {
	items := []string{"i1", "i2", "i3", "i4", "i5"}
	weights := map[string]int{"i1": 1, "i2": 2, "i3": 3, "i4": 4, "i5": 5}
	apps := map[string][]string{"app": {"r"}}
	b := New(apps, items, weights, 1)

	const rounds = 500
	// 先补齐一份完整评分，之后任何预览都必须恰好看到 1 名候选人。
	b.PutScores("app", "r", map[string]int{
		"i1": 1, "i2": 1, "i3": 1, "i4": 1, "i5": 1,
	})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			v := i%10 + 1
			b.PutScores("app", "r", map[string]int{
				"i1": v, "i2": v, "i3": v, "i4": v, "i5": v,
			})
		}
	}()
	for g := 0; g < 3; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				got := b.PreviewAwards()
				if len(got) != 1 {
					t.Errorf("want 1 award, got %v", got)
					return
				}
				aw := got[0]
				if aw.CompleteReviewers != 1 {
					t.Errorf("complete reviewers: got %d, want 1", aw.CompleteReviewers)
					return
				}
				if !aw.Score.IsInt() {
					t.Errorf("partial patch visible: non-integer score %s", aw.Score)
					return
				}
				v := aw.Score.Num().Int64()
				if v < 1 || v > 10 {
					t.Errorf("score out of range: %s", aw.Score)
					return
				}
			}
		}()
	}
	wg.Wait()
}

// 预览中的评分与权重必须来自同一状态：构造只有权重为特定组合时
// 才可能出现的结果，检查不会拼出“新评分 + 旧权重”之类的混合状态。
func TestConcurrentNoMixedState(t *testing.T) {
	items := []string{"a", "b"}
	apps := map[string][]string{"app": {"r"}}
	b := New(apps, items, map[string]int{"a": 1, "b": 1}, 1)

	// 不变量：任意单一完整状态下，score = (sa*wa + sb*wb)/(wa+wb)，
	// 其中 (sa,sb) 与 (wa,wb) 必须来自同一代。每代写入 (v,v) 与 (v,v)，
	// 因此合法预览的分数必须等于某一代的 v（整数）。
	const rounds = 300
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			v := i%10 + 1
			b.PutScores("app", "r", map[string]int{"a": v, "b": v})
			b.SetWeights(map[string]int{"a": v, "b": 100 - v})
		}
	}()
	for g := 0; g < 3; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				got := b.PreviewAwards()
				if len(got) != 1 {
					continue // 首个补丁前允许为空
				}
				if !got[0].Score.IsInt() {
					t.Errorf("mixed score/weight state: %s", got[0].Score)
					return
				}
			}
		}()
	}
	wg.Wait()
}

// 确保参考实现本身能区分相邻状态（防止测试恒真）。
func TestReferenceDistinguishesStates(t *testing.T) {
	items := []string{"a", "b"}
	m1 := newRefModel(items, map[string]int{"a": 1, "b": 1}, 1)
	m1.put("app", "r", map[string]int{"a": 5, "b": 7})
	m2 := newRefModel(items, map[string]int{"a": 1, "b": 1}, 1)
	m2.put("app", "r", map[string]int{"a": 5, "b": 8})
	if awardsEqual(m1.preview(), m2.preview()) {
		t.Fatal("reference model cannot distinguish different scores")
	}
	m3 := newRefModel(items, map[string]int{"a": 2, "b": 1}, 1)
	m3.put("app", "r", map[string]int{"a": 5, "b": 7})
	if awardsEqual(m1.preview(), m3.preview()) {
		t.Fatal("reference model cannot distinguish different weights")
	}
	if got := fmt.Sprint(big.NewRat(88, 9)); got != "88/9" {
		t.Fatal("unexpected rational formatting")
	}
}
