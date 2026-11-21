package main

import (
	"context"
	"crypto/rand"
	"errors"
	"sync"
	"time"
)

const legacyCredentialReviewTTL = 5 * time.Minute

type legacyCredentialWeb struct {
	provider string
	copy     func(context.Context, legacyCredentialPlan, string, []byte) error
	mu       sync.Mutex
	pending  *legacyCredentialWebReview
	last     *legacyCredentialWebReview
}

type legacyCredentialWebReview struct {
	Token                                         string
	Plan                                          legacyCredentialPlan
	request                                       legacyConfigImport
	base                                          []byte
	sourceID, sourcePath, revision, draftRevision string
	expires                                       time.Time
}

func (review *legacyCredentialWebReview) matchesSource(view *configWebPage) bool {
	return review.sourceID == view.Source.ID && review.sourcePath == view.Source.Path &&
		review.revision == view.Source.Revision && review.revision == view.Revision
}

func (legacy *legacyCredentialWeb) apply(ctx context.Context, view *configWebPage, action, token string, candidate []byte, editErr error) ([]byte, error) {
	legacy.mu.Lock()
	defer legacy.mu.Unlock()
	if legacy.pending != nil && !time.Now().Before(legacy.pending.expires) {
		legacy.pending = nil
	}
	review := legacy.pending
	if review != nil && review.Token != token {
		review = nil
	}
	switch action {
	case "preview-legacy-credential":
		if editErr != nil {
			return candidate, editErr
		}
		if view.Revision != view.Source.Revision {
			return candidate, errors.New("配置来源已更新，请重新载入所选配置后预览凭据复制")
		}
		base := candidate
		if last := legacy.last; last != nil && last.matchesSource(view) && last.draftRevision == configRevision(candidate) {
			// The imported draft already references the new state directory.
			// Reuse its base even after canceling or expiring the confirmation.
			base = last.base
			view.LegacyReview = review
		}
		imported, plan, err := view.LegacyImport.preview(base)
		if err != nil {
			return candidate, err
		}
		if !plan.Inline && legacy.provider == "" {
			return candidate, errors.New("当前工作台找不到可用的旧桥程序。可指定已有私有凭据文件，然后选择「导入草稿并查看差异」")
		}
		legacy.pending = &legacyCredentialWebReview{
			Token: rand.Text(), Plan: plan, request: *view.LegacyImport, base: base,
			sourceID: view.Source.ID, sourcePath: view.Source.Path, revision: view.Revision,
			draftRevision: configRevision(imported), expires: time.Now().Add(legacyCredentialReviewTTL),
		}
		legacy.last = legacy.pending
		view.LegacyReview = legacy.pending
		return imported, nil
	case "confirm-legacy-credential":
		if review == nil {
			return candidate, errors.New("请先预览 App Secret 复制，预览可能已过期或取消")
		}
		if !review.matchesSource(view) {
			legacy.pending = nil
			return candidate, errors.New("所选配置来源或版本已变化，请重新预览凭据复制")
		}
		if editErr != nil || review.draftRevision != configRevision(candidate) {
			legacy.pending = nil
			return candidate, errors.New("候选配置已变化，请重新预览凭据复制")
		}
		view.LegacyReview = review
		if review.request != *view.LegacyImport {
			return candidate, errors.New("导入表单已变化，请重新预览凭据复制")
		}
		var err error
		if legacy.copy != nil {
			err = legacy.copy(ctx, review.Plan, legacy.provider, candidate)
		} else {
			err = review.Plan.copy(ctx, legacy.provider, candidate)
		}
		if err != nil {
			return candidate, err
		}
		legacy.pending, legacy.last, view.LegacyReview, view.LegacyImport = nil, nil, nil, nil
		view.LegacyCopied = review.Plan.Destination
		return candidate, nil
	default:
		// Cancel or leave the review through another draft action.
		if review != nil {
			legacy.pending = nil
		}
		return candidate, editErr
	}
}
