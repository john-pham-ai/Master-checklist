package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/john-pham-ai/Master-checklist/confluence"
)

// When a submission's screenshots/clips fail to upload to Confluence, the
// tester gets one more chance: the failed files are held in memory (see
// retryStore) and the confirmation page offers a Retry button that calls
// POST /api/retry_uploads?id=… to upload them again. This covers transient
// Confluence failures (5xx, timeouts). It cannot cover an instance restart
// or scale-to-zero eviction — then the retry reports "unavailable" and the
// tester attaches the files manually.

const (
	// retryTTL bounds how long failed uploads are held after a submission.
	retryTTL = 10 * time.Minute
	// maxRetryPackages bounds how many submissions can hold retry files at
	// once; the oldest is evicted first (a busy min-instances=0 deployment
	// sees one submission at a time in practice).
	maxRetryPackages = 8
	// maxRetryBytes bounds the total memory the store will use; a submission
	// bigger than this just doesn't get a Retry button (screen recordings can
	// be hundreds of MB, which is too much to sit in RAM "just in case").
	maxRetryBytes = 128 << 20
)

// retryFile is one attachment whose upload failed, with its bytes held so
// the upload can be attempted again without the browser re-sending them.
type retryFile struct {
	PageFilename string
	ContentType  string
	Data         []byte
}

// retryPackage is one submission's failed uploads, addressable by id from
// the confirmation page.
type retryPackage struct {
	PageID  string
	Files   []retryFile
	Created time.Time
}

type retryStore struct {
	mu       sync.Mutex
	packages map[string]*retryPackage
	order    []string // insertion order, for eviction and expiry
	bytes    int64
}

func newRetryStore() *retryStore {
	return &retryStore{packages: map[string]*retryPackage{}}
}

// put holds one package. It returns false (and holds nothing) when the
// package would blow the memory budget — the confirmation page then offers
// only the manual fallback.
func (s *retryStore) put(id, pageID string, files []retryFile) bool {
	var size int64
	for _, f := range files {
		size += int64(len(f.Data))
	}
	if size > maxRetryBytes {
		return false
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(time.Now())
	s.evictLocked(size)

	s.packages[id] = &retryPackage{PageID: pageID, Files: files, Created: time.Now()}
	s.order = append(s.order, id)
	s.bytes += size
	return true
}

// get returns the package for id, or nil when it expired, was evicted, or
// the instance restarted (the map is in-memory only — that's exactly the
// "unavailable" case the confirmation page explains).
func (s *retryStore) get(id string) *retryPackage {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(time.Now())
	return s.packages[id]
}

// update shrinks a package to the files that are still failing after a
// partial retry, so the next retry only re-attempts those.
func (s *retryStore) update(id string, stillFailing []retryFile) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pkg, ok := s.packages[id]
	if !ok {
		return
	}
	var before int64
	for _, f := range pkg.Files {
		before += int64(len(f.Data))
	}
	pkg.Files = stillFailing
	var after int64
	for _, f := range stillFailing {
		after += int64(len(f.Data))
	}
	s.bytes += after - before
}

// delete removes a package (after a fully successful retry).
func (s *retryStore) delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if pkg, ok := s.packages[id]; ok {
		for _, f := range pkg.Files {
			s.bytes -= int64(len(f.Data))
		}
		delete(s.packages, id)
		for i, o := range s.order {
			if o == id {
				s.order = append(s.order[:i], s.order[i+1:]...)
				break
			}
		}
	}
}

// pruneLocked drops expired packages (and any that only *contained* expired
// entries, none exist). Callers must hold s.mu.
func (s *retryStore) pruneLocked(now time.Time) {
	for _, id := range append([]string(nil), s.order...) {
		pkg, ok := s.packages[id]
		if !ok {
			continue
		}
		if now.Sub(pkg.Created) > retryTTL {
			for _, f := range pkg.Files {
				s.bytes -= int64(len(f.Data))
			}
			delete(s.packages, id)
			for i, o := range s.order {
				if o == id {
					s.order = append(s.order[:i], s.order[i+1:]...)
					break
				}
			}
		}
	}
}

// evictLocked makes room for `incoming` bytes by dropping the oldest
// packages. Callers must hold s.mu.
func (s *retryStore) evictLocked(incoming int64) {
	for s.bytes+incoming > maxRetryBytes || len(s.order) >= maxRetryPackages {
		if len(s.order) == 0 {
			return
		}
		oldest := s.order[0]
		pkg := s.packages[oldest]
		if pkg != nil {
			for _, f := range pkg.Files {
				s.bytes -= int64(len(f.Data))
			}
		}
		delete(s.packages, oldest)
		s.order = s.order[1:]
	}
}

// makeRetryUploadsHandler serves POST /api/retry_uploads?id=<retryId>. The
// browser calls it from the confirmation page's Retry button. It re-uploads
// the held files to the same Confluence page and reports what (if anything)
// is still failing.
func makeRetryUploadsHandler(cfg config, store *retryStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		id := r.URL.Query().Get("id")
		pkg := store.get(id)
		w.Header().Set("Content-Type", "application/json")
		if pkg == nil {
			json.NewEncoder(w).Encode(map[string]interface{}{"unavailable": true})
			return
		}
		if len(pkg.Files) == 0 {
			store.delete(id)
			json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "failed": []string{}})
			return
		}

		token, err := cfg.Token.Get(r.Context())
		if err != nil {
			log.Printf("retry uploads: failed to load Confluence token: %v", err)
			json.NewEncoder(w).Encode(map[string]interface{}{"unavailable": true})
			return
		}
		client := confluence.NewClient(cfg.BaseURL, cfg.SpaceKey, cfg.ParentPageID, cfg.BotEmail, token)

		var stillFailing []retryFile
		var failedNames []string
		for _, f := range pkg.Files {
			err := client.UploadAttachment(pkg.PageID, f.PageFilename, f.ContentType, bytes.NewReader(f.Data))
			if err != nil {
				log.Printf("retry uploads: %q still failing: %v", f.PageFilename, err)
				stillFailing = append(stillFailing, f)
				failedNames = append(failedNames, f.PageFilename)
			}
		}
		if len(stillFailing) == 0 {
			store.delete(id)
			json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "failed": []string{}})
			return
		}
		store.update(id, stillFailing)
		json.NewEncoder(w).Encode(map[string]interface{}{"ok": false, "failed": failedNames})
	}
}

// newRetryID returns a random-enough id for one retry package. It only needs
// to be unguessable between submissions, not cryptographic.
func newRetryID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
