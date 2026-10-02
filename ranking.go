// Package scholarship 提供奖学金评审的进程内排名库。
package scholarship

import (
	"math/big"
	"sort"
	"sync"
)

// Award 表示一名获奖候选人。
type Award struct {
	ApplicationID string
	// Score 为精确有理数分数：先对每名完整评审求加权平均，再等权平均。
	Score *big.Rat
	// CompleteReviewers 为参与计算的完整评审人数。
	CompleteReviewers int
}

// Board 是评审排名板。所有操作可线性化。
type Board struct {
	mu           sync.RWMutex
	applications map[string]map[string]map[string]int // app -> reviewer -> item -> score
	reviewers    map[string]map[string]struct{}       // app -> assigned reviewers
	items        []string
	itemSet      map[string]struct{}
	weights      map[string]int
	k            int
}

// New 创建排名板。applicationReviewers 为每份申请已分配的评审集合，
// items 为 1 至 5 个评分项，weights 为各项初始权重（1..100），k 为名额。
func New(applicationReviewers map[string][]string, items []string, weights map[string]int, k int) *Board {
	if len(items) < 1 || len(items) > 5 {
		panic("scholarship: items must contain 1 to 5 entries")
	}
	itemSet := make(map[string]struct{}, len(items))
	for _, it := range items {
		if _, dup := itemSet[it]; dup {
			panic("scholarship: duplicate score item " + it)
		}
		itemSet[it] = struct{}{}
	}
	if len(weights) != len(items) {
		panic("scholarship: weights must cover exactly the score items")
	}
	w := make(map[string]int, len(weights))
	for it, v := range weights {
		if _, ok := itemSet[it]; !ok {
			panic("scholarship: weight for unknown item " + it)
		}
		if v < 1 || v > 100 {
			panic("scholarship: weight out of range 1..100")
		}
		w[it] = v
	}
	if k < 0 {
		panic("scholarship: k must be non-negative")
	}
	apps := make(map[string]map[string]map[string]int, len(applicationReviewers))
	revs := make(map[string]map[string]struct{}, len(applicationReviewers))
	for app, reviewers := range applicationReviewers {
		apps[app] = make(map[string]map[string]int)
		set := make(map[string]struct{}, len(reviewers))
		for _, r := range reviewers {
			set[r] = struct{}{}
		}
		revs[app] = set
	}
	return &Board{
		applications: apps,
		reviewers:    revs,
		items:        append([]string(nil), items...),
		itemSet:      itemSet,
		weights:      w,
		k:            k,
	}
}

// PutScores 原子提交某评审对某申请的若干评分项补丁（每项 1..10）。
func (b *Board) PutScores(applicationID, reviewerID string, changes map[string]int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	assigned, ok := b.reviewers[applicationID]
	if !ok {
		panic("scholarship: unknown application " + applicationID)
	}
	if _, ok := assigned[reviewerID]; !ok {
		panic("scholarship: reviewer " + reviewerID + " not assigned to " + applicationID)
	}
	for it, v := range changes {
		if _, ok := b.itemSet[it]; !ok {
			panic("scholarship: unknown score item " + it)
		}
		if v < 1 || v > 10 {
			panic("scholarship: score out of range 1..10")
		}
	}
	scores, ok := b.applications[applicationID][reviewerID]
	if !ok {
		scores = make(map[string]int, len(changes))
		b.applications[applicationID][reviewerID] = scores
	}
	for it, v := range changes {
		scores[it] = v
	}
}

// SetWeights 原子替换全部评分项权重，对之后的预览生效。
func (b *Board) SetWeights(weights map[string]int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(weights) != len(b.items) {
		panic("scholarship: weights must cover exactly the score items")
	}
	w := make(map[string]int, len(weights))
	for it, v := range weights {
		if _, ok := b.itemSet[it]; !ok {
			panic("scholarship: weight for unknown item " + it)
		}
		if v < 1 || v > 100 {
			panic("scholarship: weight out of range 1..100")
		}
		w[it] = v
	}
	b.weights = w
}

// PreviewAwards 返回最多 K 名获奖候选人，对应某一时刻的完整状态。
func (b *Board) PreviewAwards() []Award {
	b.mu.RLock()
	defer b.mu.RUnlock()
	weightSum := 0
	for _, it := range b.items {
		weightSum += b.weights[it]
	}
	denominator := new(big.Rat).SetInt64(int64(weightSum))
	awards := make([]Award, 0, len(b.applications))
	for app, reviewers := range b.applications {
		total := new(big.Rat)
		complete := 0
		for _, scores := range reviewers {
			if len(scores) != len(b.items) {
				continue
			}
			weighted := new(big.Rat)
			for _, it := range b.items {
				v, ok := scores[it]
				if !ok {
					weighted = nil
					break
				}
				term := new(big.Rat).SetInt64(int64(v * b.weights[it]))
				weighted.Add(weighted, term)
			}
			if weighted == nil {
				continue
			}
			weighted.Quo(weighted, denominator)
			total.Add(total, weighted)
			complete++
		}
		if complete == 0 {
			continue
		}
		score := new(big.Rat).Quo(total, new(big.Rat).SetInt64(int64(complete)))
		awards = append(awards, Award{ApplicationID: app, Score: score, CompleteReviewers: complete})
	}
	sort.Slice(awards, func(i, j int) bool {
		c := awards[i].Score.Cmp(awards[j].Score)
		if c != 0 {
			return c > 0
		}
		return awards[i].ApplicationID < awards[j].ApplicationID
	})
	if len(awards) > b.k {
		awards = awards[:b.k]
	}
	return awards
}
