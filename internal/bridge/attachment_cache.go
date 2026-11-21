package bridge

import (
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"time"

	"github.com/HessianZ/t3-bridge/internal/localfile"
)

const (
	maxTurnAttachments      = 10
	maxNativeAttachments    = 8 // Both v0.1.10 MCP submission tools limit their attachment arrays.
	maxAttachmentFileBytes  = 50 << 20
	maxNativeImageBytes     = 10 << 20
	maxCachedImageBytes     = 25 << 20
	maxTurnAttachmentBytes  = 100 << 20
	maxNativeTurnImageBytes = 80 << 20
)

var errAttachmentSize = errors.New("attachment outside bounds")

func attachmentCountLimit(cached bool) int {
	if cached {
		return maxTurnAttachments
	}
	return maxNativeAttachments
}

func attachmentSizeLimit(im Image, cached bool) int {
	if im.Kind == "file" {
		return maxAttachmentFileBytes
	}
	if cached {
		return maxCachedImageBytes
	}
	return maxNativeImageBytes
}

func nativeImageMIME(mimeType string) bool {
	return slices.Contains([]string{"image/png", "image/jpeg", "image/gif", "image/webp"}, mimeType)
}

func attachmentMetadata(im Image, data []byte, cached bool) (Attachment, error) {
	if len(data) == 0 || len(data) > attachmentSizeLimit(im, cached) {
		return Attachment{}, errAttachmentSize
	}
	contentType, _, _ := mime.ParseMediaType(http.DetectContentType(data))
	kind := im.Kind
	if kind == "" {
		kind = "image"
	}
	if kind != "image" && kind != "file" {
		return Attachment{}, errors.New("unsupported attachment type")
	}
	if kind == "image" && !nativeImageMIME(contentType) {
		return Attachment{}, errors.New("unsupported image type")
	}
	return Attachment{Type: kind, Name: attachmentName(im.Name, kind), MIME: contentType, Size: len(data)}, nil
}

type cachedFollowupAttachment struct {
	Path string `json:"path"`
	Name string `json:"name"`
	Type string `json:"type"`
}

const followupCacheDir = "followup-attachments"

var safeCacheExtension = regexp.MustCompile(`^\.[a-zA-Z0-9]{1,12}$`)

func cacheRequestKey(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func (e *Engine) cacheFollowupAttachment(requestID string, index int, im Image, data []byte) (cachedFollowupAttachment, error) {
	meta, err := attachmentMetadata(im, data, true)
	if err != nil {
		return cachedFollowupAttachment{}, err
	}
	if !cacheRequestKey(requestID) {
		return cachedFollowupAttachment{}, errors.New("invalid attachment cache request identity")
	}
	root := filepath.Join(e.Store.dir, followupCacheDir)
	if err := localfile.MkdirAllPrivate(root); err != nil {
		return cachedFollowupAttachment{}, err
	}
	dir := filepath.Join(root, requestID)
	if err := localfile.MkdirAllPrivate(dir); err != nil {
		return cachedFollowupAttachment{}, err
	}
	ext := filepath.Ext(meta.Name)
	if meta.Type == "image" {
		ext = map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif", "image/webp": ".webp"}[meta.MIME]
	} else if !safeCacheExtension.MatchString(ext) {
		ext = ".bin"
	}
	path := filepath.Join(dir, fmt.Sprintf("%02d%s", index+1, ext))
	if _, err := localfile.WritePrivateAtomic(path, data, false); err != nil {
		return cachedFollowupAttachment{}, err
	}
	return cachedFollowupAttachment{Path: path, Name: meta.Name, Type: meta.Type}, nil
}

func (e *Engine) pruneFollowupAttachments() {
	now := e.now()
	if now.Before(e.nextAttachmentCleanup) {
		return
	}
	e.nextAttachmentCleanup = now.Add(time.Hour)
	root := filepath.Join(e.Store.dir, followupCacheDir)
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() {
		return
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	active := map[string]bool{}
	for _, t := range e.Store.snapshot().Tasks {
		if !terminal(t.Phase) || t.NeedsReconcile {
			active[t.RequestID] = true
		}
	}
	cutoff := now.Add(-24 * time.Hour)
	for _, entry := range entries {
		if !entry.IsDir() || !cacheRequestKey(entry.Name()) || active[entry.Name()] {
			continue
		}
		if info, err := entry.Info(); err == nil && info.ModTime().Before(cutoff) {
			_ = os.RemoveAll(filepath.Join(root, entry.Name()))
		}
	}
}
