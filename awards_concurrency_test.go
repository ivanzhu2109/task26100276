package scholarship

import (
	"fmt"
	"math/big"
	"math/rand"
	"sort"
	"sync"
	"testing"
)

// fullState 是某一完整已提交状态的快照。
type fullState struct {
	weights map[string]int
	scores  map[string]map[string]map[string]int
}

func copyScores(src map[string]map[string]map[string]int) map[string]map[string]map[string]int {
	out := make(map[string]map[string]map[string]int, len(src))
	for app, byRev := range src {
		out[app] = make(map[string]map[string]int, len(byRev))
		for rev, items := range byRev {
			out[app][rev] = make(map[string]int, len(items))
			for item, v := range items {
				out[app][rev][item] = v
			}
		}
	}
	return out
}

// previewOf 是从完整状态计算获奖名单的独立参考实现。
func previewOf(items []string, k int, weights map[string]int, scores map[string]map[string]map[string]int) []Award {
	weightSum := 0
	for _, it := range items {
		weightSum += weights[it]
	}
	var out []Award
	for appID, byReviewer := range scores {
		total := new(big.Rat)
		complete := 0
		for _, revItems := range byReviewer {
			if len(revItems) != len(items) {
				continue
			}
			sum := new(big.Rat)
			for _, it := range items {
				sum.Add(sum, new(big.Rat).SetInt64(int64(weights[it]*revItems[it])))
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
	if len(out) > k {
		out = out[:k]
	}
	if out == nil {
		out = []Award{}
	}
	return out
}

func awardsEqual(a, b []Award) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ApplicationID != b[i].ApplicationID ||
			a[i].CompleteReviewers != b[i].CompleteReviewers ||
			a[i].Score.Cmp(b[i].Score) != 0 {
			return false
		}
	}
	return true
}

// TestConcurrentLinearizable 并发交错提交评分补丁、替换权重与执行预览，
// 验证每份预览都对应其调用区间内某个已提交的完整状态。
func TestConcurrentLinearizable(t *testing.T) {
	items := []string{"a", "b", "c"}
	k := 3
	apps := []string{"app0", "app1", "app2", "app3", "app4", "app5"}
	reviewers := map[string][]string{}
	for _, app := range apps {
		reviewers[app] = []string{"r0", "r1", "r2"}
	}
	initWeights := map[string]int{"a": 1, "b": 1, "c": 1}

	b, err := NewBoard(apps, reviewers, items, initWeights, k)
	if err != nil {
		t.Fatal(err)
	}

	// history[i] 为第 i 次提交后的完整状态，history[0] 为初始状态。
	// 写操作在 recMu 保护下完成「库调用 + 状态记录」，因此 history 的顺序
	// 就是库内写操作的线性化顺序，且预览在写操作线性化之后读到的序号
	// 一定已包含该写操作的记录。
	var (
		recMu   sync.Mutex
		current = fullState{weights: copyWeights(initWeights), scores: copyScores(nil)}
		history = []fullState{{weights: copyWeights(initWeights), scores: copyScores(nil)}}
	)
	seqNow := func() int {
		recMu.Lock()
		defer recMu.Unlock()
		return len(history) - 1
	}

	const (
		writers       = 4
		putsPerWriter = 200
		weightSetters = 2
		setsPerSetter = 50
		previewers    = 3
		previewsEach  = 300
	)

	var wg sync.WaitGroup

	// 评分补丁写入：补丁含随机子集的多个评分项，以检验补丁原子性。
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(seed int64) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(seed))
			for i := 0; i < putsPerWriter; i++ {
				app := apps[rng.Intn(len(apps))]
				rev := fmt.Sprintf("r%d", rng.Intn(3))
				changes := map[string]int{}
				for _, item := range items {
					if rng.Intn(2) == 0 {
						changes[item] = 1 + rng.Intn(10)
					}
				}
				if len(changes) == 0 {
					changes[items[rng.Intn(len(items))]] = 1 + rng.Intn(10)
				}
				recMu.Lock()
				if err := b.PutScores(app, rev, changes); err != nil {
					recMu.Unlock()
					t.Errorf("PutScores: %v", err)
					return
				}
				cur := current.scores[app][rev]
				if cur == nil {
					cur = map[string]int{}
					if current.scores[app] == nil {
						current.scores[app] = map[string]map[string]int{}
					}
					current.scores[app][rev] = cur
				}
				for item, v := range changes {
					cur[item] = v
				}
				history = append(history, fullState{weights: copyWeights(current.weights), scores: copyScores(current.scores)})
				recMu.Unlock()
			}
		}(int64(w) + 1)
	}

	// 权重替换。
	for w := 0; w < weightSetters; w++ {
		wg.Add(1)
		go func(seed int64) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(seed))
			for i := 0; i < setsPerSetter; i++ {
				nw := map[string]int{}
				for _, item := range items {
					nw[item] = 1 + rng.Intn(100)
				}
				recMu.Lock()
				if err := b.SetWeights(nw); err != nil {
					recMu.Unlock()
					t.Errorf("SetWeights: %v", err)
					return
				}
				current.weights = copyWeights(nw)
				history = append(history, fullState{weights: copyWeights(current.weights), scores: copyScores(current.scores)})
				recMu.Unlock()
			}
		}(int64(100 + w))
	}

	// 预览：验证结果对应调用区间内某个已提交完整状态。
	for p := 0; p < previewers; p++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < previewsEach; i++ {
				seqStart := seqNow()
				got := b.PreviewAwards()
				seqEnd := seqNow()

				recMu.Lock()
				matched := false
				for l := seqStart; l <= seqEnd && l < len(history); l++ {
					if awardsEqual(got, previewOf(items, k, history[l].weights, history[l].scores)) {
						matched = true
						break
					}
				}
				recMu.Unlock()
				if !matched {
					t.Errorf("预览结果不对应调用区间 [%d,%d] 内任何已提交完整状态: %v", seqStart, seqEnd, got)
					return
				}
			}
		}()
	}
	wg.Wait()
}
