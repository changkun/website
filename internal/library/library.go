// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// Package library serves changkun.de/library: Changkun's Zotero library,
// browsable by the owner and shareable by topic.
//
// Zotero is the only source of entries and topics. The server keeps a cache of
// the library that it can rebuild at any time (cache.json), synced from the
// Zotero Web API on a timer and on demand. The only data it owns is shares and
// the comments and suggestions on them, a few files in state/. See the design
// doc for the reasoning: https://claude.ai/artifact/4jUmk2S2wv4XXSe6fsWab2
package library

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"changkun.de/x/website/internal/owner"
)

// Prefix is the path the library is mounted at. The mux does not strip it.
const Prefix = "/library/"

// Service is the configured library. A nil *Service has no Zotero
// credentials, and Handler answers it with 503.
type Service struct {
	log      *log.Logger
	zotero   *zoteroClient
	dir      string // holds cache.json and state/
	interval time.Duration
	store    *store
	owner    *owner.Owner

	ix atomic.Pointer[index]

	syncMu sync.Mutex      // one sync at a time
	runCtx context.Context // from Run; Sync now runs in it, so shutdown stops it

	statusMu  sync.Mutex
	lastOK    time.Time // last successful sync
	lastErr   string
	lastErrAt time.Time
	notBefore time.Time // Zotero asked us to wait until then
	syncing   [2]int    // items fetched and total, while a sync fetches items
}

// New configures the library from the environment. It returns nil if
// ZOTERO_USER_ID or ZOTERO_API_KEY is unset, so a host without them still
// serves the site.
//
//	ZOTERO_USER_ID, ZOTERO_API_KEY  the library and a read-only key
//	LIBRARY_DIR                     holds cache.json and state/; default /data
//	SYNC_INTERVAL                   default 15m
//	ZOTERO_API_URL                  default https://api.zotero.org
//
// o decides which requests are the owner's.
func New(l *log.Logger, o *owner.Owner) (*Service, error) {
	userID, key := os.Getenv("ZOTERO_USER_ID"), os.Getenv("ZOTERO_API_KEY")
	if userID == "" || key == "" {
		l.Println("library is unconfigured, ZOTERO_USER_ID and ZOTERO_API_KEY are required")
		return nil, nil
	}
	interval := 15 * time.Minute
	if s := os.Getenv("SYNC_INTERVAL"); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil || d < time.Minute {
			return nil, fmt.Errorf("SYNC_INTERVAL must be a duration of at least 1m, got %q", s)
		}
		interval = d
	}
	dir := cmp.Or(os.Getenv("LIBRARY_DIR"), "/data")
	st, err := openStore(filepath.Join(dir, "state"))
	if err != nil {
		return nil, fmt.Errorf("library state: %w", err)
	}
	s := &Service{
		log:      l,
		zotero:   &zoteroClient{baseURL: os.Getenv("ZOTERO_API_URL"), userID: userID, key: key},
		dir:      dir,
		interval: interval,
		store:    st,
		owner:    o,
	}
	s.ix.Store(newIndex(s.loadCache()))
	return s, nil
}

// loadCache reads cache.json. A missing or unreadable cache is empty, and the
// first sync fetches the whole library.
func (s *Service) loadCache() cacheFile {
	var c cacheFile
	name := filepath.Join(s.dir, "cache.json")
	raw, err := os.ReadFile(name)
	if errors.Is(err, fs.ErrNotExist) {
		return c
	}
	if err == nil {
		err = json.Unmarshal(raw, &c)
	}
	if err != nil {
		s.log.Printf("library: cache.json is unreadable, a full sync rebuilds it: %v", err)
		return cacheFile{}
	}
	if c.Format != cacheFormat {
		s.log.Printf("library: cache.json is format %d, not %d; a full sync rebuilds it", c.Format, cacheFormat)
		return cacheFile{}
	}
	// The cache was written by the last successful sync, so the status line
	// can say when that was, even after a restart.
	if fi, err := os.Stat(name); err == nil {
		s.lastOK = fi.ModTime()
	}
	return c
}

// Run syncs now and then every interval until ctx is done.
func (s *Service) Run(ctx context.Context) {
	if s == nil {
		return
	}
	s.runCtx = ctx
	go func() {
		t := time.NewTicker(s.interval)
		defer t.Stop()
		for {
			if err := s.Sync(ctx); err != nil && ctx.Err() == nil {
				s.log.Printf("library: sync: %v", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

// Sync brings the cache up to date with Zotero. If a sync is running, it
// waits for that one and then runs its own, which is then quick. On any error
// the site keeps serving the last good index.
func (s *Service) Sync(ctx context.Context) (err error) {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("sync panicked: %v", p)
		}
		s.record(err)
	}()
	s.zotero.progress = func(done, total int) {
		s.statusMu.Lock()
		s.syncing = [2]int{done, total}
		s.statusMu.Unlock()
		if total > pageSize {
			s.log.Printf("library: sync: %d of %d items", done, total)
		}
	}

	s.statusMu.Lock()
	wait := time.Until(s.notBefore)
	s.statusMu.Unlock()
	if wait > 0 {
		return fmt.Errorf("Zotero asked to wait, next sync after %s", wait.Round(time.Second))
	}
	if wait := time.Until(s.zotero.notBefore); wait > 0 {
		return fmt.Errorf("Zotero asked to wait, next sync after %s", wait.Round(time.Second))
	}

	old := s.ix.Load()
	start := time.Now()
	ch, err := s.zotero.fetch(ctx, old.cache.Version)
	if err != nil {
		if wait, ok := isBackoff(err); ok {
			s.statusMu.Lock()
			s.notBefore = time.Now().Add(wait)
			s.statusMu.Unlock()
		}
		return err
	}
	if ch.version == old.cache.Version && len(ch.items) == 0 && len(ch.collections) == 0 &&
		len(ch.deleted.Items) == 0 && len(ch.deleted.Collections) == 0 {
		return nil
	}

	next, bad := apply(old.cache, ch)
	for _, k := range bad {
		s.log.Printf("library: item %s does not parse, skipped", k)
	}
	ix := newIndex(next)
	for _, keys := range ix.duplicateDOIs() {
		s.log.Printf("library: items %v have the same DOI; merge them in Zotero", keys)
	}
	raw, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if err := writeFile(filepath.Join(s.dir, "cache.json"), raw); err != nil {
		return fmt.Errorf("save cache: %w", err)
	}
	s.ix.Store(ix)
	s.log.Printf("library: synced to version %d in %s: %d collections, %d items changed; %d entries",
		next.Version, time.Since(start).Round(time.Millisecond), len(ch.collections), len(ch.items), len(ix.entries))
	return nil
}

func (s *Service) record(err error) {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	s.syncing = [2]int{}
	if err == nil {
		s.lastOK = time.Now()
		return
	}
	s.lastErr = err.Error()
	s.lastErrAt = time.Now()
}

// status returns the time of the last successful sync and the last error if it
// came after it.
func (s *Service) status() (ok time.Time, errMsg string, errAt time.Time) {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	if s.lastErrAt.After(s.lastOK) {
		return s.lastOK, s.lastErr, s.lastErrAt
	}
	return s.lastOK, "", time.Time{}
}

// progress returns the items fetched so far and the total while a sync
// fetches items, and zeros otherwise.
func (s *Service) progress() (done, total int) {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	return s.syncing[0], s.syncing[1]
}

// Handler serves the library below Prefix.
func Handler(s *Service) http.Handler {
	if s == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "library is not configured", http.StatusServiceUnavailable)
		})
	}
	return s.routes()
}
