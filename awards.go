// Package scholarship 提供奖学金评审的进程内排名库。
package scholarship

import (
	"errors"
	"fmt"
	"math/big"
	"sort"
	"sync"
)

// Award 是 PreviewAwards 返回的一名获奖候选人。
type Award struct {
	ApplicationID string
	// Score 为精确有理数分数：先对每名完整评审求加权平均，
	// 再对所有完整评审等权平均。
	Score *big.Rat
	// CompleteReviewers 为参与计算的完整评审人数。
	CompleteReviewers int
}

// Board 保存评审状态，所有操作可线性化。
type Board struct {
	mu        sync.RWMutex
	items     []string                             // 固定评分项，1 至 5 个
	k         int                                  // 名额
	weights   map[string]int                       // 当前权重
	reviewers map[string]map[string]bool           // 每份申请已分配的评审
	scores    map[string]map[string]map[string]int // 申请 -> 评审 -> 评分项 -> 分值
}

// NewBoard 创建评审板。items 为 1 至 5 个评分项，weights 每项为 1 至 100，
// k 为获奖名额。申请集合、评审分配与 k 之后不变。
func NewBoard(applicationIDs []string, reviewers map[string][]string, items []string, weights map[string]int, k int) (*Board, error) {
	if len(items) < 1 || len(items) > 5 {
		return nil, fmt.Errorf("items 数量必须为 1 至 5，当前 %d", len(items))
	}
	seen := make(map[string]bool, len(items))
	for _, it := range items {
		if it == "" {
			return nil, errors.New("评分项不能为空字符串")
		}
		if seen[it] {
			return nil, fmt.Errorf("评分项重复: %q", it)
		}
		seen[it] = true
	}
	if err := validateWeights(items, weights); err != nil {
		return nil, err
	}
	if k < 1 {
		return nil, fmt.Errorf("名额 k 必须为正整数，当前 %d", k)
	}
	if len(applicationIDs) == 0 {
		return nil, errors.New("申请集合不能为空")
	}
	b := &Board{
		items:     append([]string(nil), items...),
		k:         k,
		weights:   copyWeights(weights),
		reviewers: make(map[string]map[string]bool, len(applicationIDs)),
		scores:    make(map[string]map[string]map[string]int, len(applicationIDs)),
	}
	for _, id := range applicationIDs {
		if id == "" {
			return nil, errors.New("申请 ID 不能为空字符串")
		}
		if _, dup := b.reviewers[id]; dup {
			return nil, fmt.Errorf("申请 ID 重复: %q", id)
		}
		b.reviewers[id] = make(map[string]bool)
		b.scores[id] = make(map[string]map[string]int)
	}
	for appID, revs := range reviewers {
		assigned, ok := b.reviewers[appID]
		if !ok {
			return nil, fmt.Errorf("评审分配指向未知申请: %q", appID)
		}
		for _, r := range revs {
			if r == "" {
				return nil, fmt.Errorf("申请 %q 的评审 ID 不能为空字符串", appID)
			}
			assigned[r] = true
		}
	}
	return b, nil
}

func validateWeights(items []string, weights map[string]int) error {
	if len(weights) != len(items) {
		return fmt.Errorf("权重必须恰好覆盖全部 %d 个评分项", len(items))
	}
	for _, it := range items {
		w, ok := weights[it]
		if !ok {
			return fmt.Errorf("缺少评分项 %q 的权重", it)
		}
		if w < 1 || w > 100 {
			return fmt.Errorf("评分项 %q 的权重 %d 不在 1 至 100", it, w)
		}
	}
	return nil
}

func copyWeights(weights map[string]int) map[string]int {
	out := make(map[string]int, len(weights))
	for k, v := range weights {
		out[k] = v
	}
	return out
}

// PutScores 原子提交某评审对某申请的若干评分项补丁，每项 1 至 10。
// 未出现的项保持原值；重复提交只保留当前分值。
func (b *Board) PutScores(applicationID, reviewerID string, changes map[string]int) error {
	assigned, ok := b.reviewers[applicationID]
	if !ok {
		return fmt.Errorf("未知申请: %q", applicationID)
	}
	if !assigned[reviewerID] {
		return fmt.Errorf("评审 %q 未分配给申请 %q", reviewerID, applicationID)
	}
	if len(changes) == 0 {
		return errors.New("补丁不能为空")
	}
	for item, v := range changes {
		if !b.isItem(item) {
			return fmt.Errorf("未知评分项: %q", item)
		}
		if v < 1 || v > 10 {
			return fmt.Errorf("评分项 %q 的分值 %d 不在 1 至 10", item, v)
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	cur := b.scores[applicationID][reviewerID]
	if cur == nil {
		cur = make(map[string]int, len(b.items))
		b.scores[applicationID][reviewerID] = cur
	}
	for item, v := range changes {
		cur[item] = v
	}
	return nil
}

// SetWeights 原子替换全部评分项的权重，对之后取得的预览视图生效。
func (b *Board) SetWeights(weights map[string]int) error {
	if err := validateWeights(b.items, weights); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.weights = copyWeights(weights)
	return nil
}

func (b *Board) isItem(item string) bool {
	for _, it := range b.items {
		if it == item {
			return true
		}
	}
	return false
}

// PreviewAwards 返回最多 K 名获奖候选人，按分数降序、同分按申请 ID 字典序。
// 返回的名单、顺序、权重、评分与完整评审人数对应同一完整状态。
func (b *Board) PreviewAwards() []Award {
	b.mu.RLock()
	defer b.mu.RUnlock()

	weightSum := 0
	for _, it := range b.items {
		weightSum += b.weights[it]
	}

	awards := make([]Award, 0, len(b.scores))
	for appID, byReviewer := range b.scores {
		total := new(big.Rat)
		complete := 0
		for _, items := range byReviewer {
			if len(items) != len(b.items) {
				continue // 残缺评审不参与
			}
			reviewerSum := new(big.Rat)
			for _, it := range b.items {
				term := new(big.Rat).SetInt64(int64(b.weights[it] * items[it]))
				reviewerSum.Add(reviewerSum, term)
			}
			reviewerSum.Quo(reviewerSum, new(big.Rat).SetInt64(int64(weightSum)))
			total.Add(total, reviewerSum)
			complete++
		}
		if complete == 0 {
			continue // 没有完整评审的申请不参与排名
		}
		score := new(big.Rat).Quo(total, new(big.Rat).SetInt64(int64(complete)))
		awards = append(awards, Award{ApplicationID: appID, Score: score, CompleteReviewers: complete})
	}

	sort.Slice(awards, func(i, j int) bool {
		if c := awards[i].Score.Cmp(awards[j].Score); c != 0 {
			return c > 0
		}
		return awards[i].ApplicationID < awards[j].ApplicationID
	})
	if len(awards) > b.k {
		awards = awards[:b.k]
	}
	return awards
}
