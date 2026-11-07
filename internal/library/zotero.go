// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package library

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// zoteroClient reads one user library from the Zotero Web API v3 with a
// read-only key.
type zoteroClient struct {
	baseURL string // API root, such as https://api.zotero.org
	userID  string
	key     string
	http    *http.Client

	notBefore time.Time // from a Backoff header; see get
}

// pageSize is the most the API returns per request.
const pageSize = 100

// backoffError is returned when Zotero asks the client to wait, by a 429 or
// 503 with Retry-After, or a Backoff header.
type backoffError struct {
	wait time.Duration
	msg  string
}

func (e *backoffError) Error() string {
	return fmt.Sprintf("%s, Zotero asks to wait %s", e.msg, e.wait)
}

// collectionJSON is one collection as the API returns it.
type collectionJSON struct {
	Key  string `json:"key"`
	Data struct {
		Name             string          `json:"name"`
		ParentCollection json.RawMessage `json:"parentCollection"` // false or a key
	} `json:"data"`
}

// itemJSON is one item as the API returns it with include=data,bibtex.
type itemJSON struct {
	Key     string          `json:"key"`
	Version int             `json:"version"`
	Data    json.RawMessage `json:"data"`
	BibTeX  string          `json:"bibtex"`
}

// deletedJSON is the reply of /deleted.
type deletedJSON struct {
	Collections []string `json:"collections"`
	Items       []string `json:"items"`
}

// changes is everything that changed in the library since a version.
type changes struct {
	version     int // the library version the changes bring the cache to
	collections []collectionJSON
	items       []itemJSON
	deleted     deletedJSON
}

// fetch returns the changes since version; 0 fetches the whole library.
//
// The returned version is the one the first response reports. A change made
// while the pages are read has a later version, so the next fetch picks it up
// again, and nothing is missed.
func (z *zoteroClient) fetch(ctx context.Context, since int) (*changes, error) {
	c := &changes{}
	since_ := strconv.Itoa(since)

	v, err := z.pages(ctx, "collections", url.Values{"since": {since_}}, func(raw json.RawMessage) error {
		var page []collectionJSON
		if err := json.Unmarshal(raw, &page); err != nil {
			return err
		}
		c.collections = append(c.collections, page...)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("collections: %w", err)
	}
	c.version = v

	// Trashed items are included, so that moving an item to the trash, which
	// changes its version, removes it here; see index.apply.
	q := url.Values{"since": {since_}, "include": {"data,bibtex"}, "includeTrashed": {"1"}}
	if _, err := z.pages(ctx, "items/top", q, func(raw json.RawMessage) error {
		var page []itemJSON
		if err := json.Unmarshal(raw, &page); err != nil {
			return err
		}
		c.items = append(c.items, page...)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("items: %w", err)
	}

	if since > 0 {
		raw, _, _, err := z.get(ctx, "deleted", url.Values{"since": {since_}})
		if err != nil {
			return nil, fmt.Errorf("deleted: %w", err)
		}
		if err := json.Unmarshal(raw, &c.deleted); err != nil {
			return nil, fmt.Errorf("deleted: %w", err)
		}
	}
	return c, nil
}

// pages reads every page of path, passing each to fn, and returns the
// library version of the first response.
func (z *zoteroClient) pages(ctx context.Context, path string, q url.Values, fn func(json.RawMessage) error) (int, error) {
	version := 0
	for start := 0; ; start += pageSize {
		q.Set("limit", strconv.Itoa(pageSize))
		q.Set("start", strconv.Itoa(start))
		raw, v, total, err := z.get(ctx, path, q)
		if err != nil {
			return 0, err
		}
		if start == 0 {
			version = v
		}
		if err := fn(raw); err != nil {
			return 0, err
		}
		if start+pageSize >= total {
			return version, nil
		}
	}
}

// get sends one GET and returns the body, the Last-Modified-Version header,
// and the Total-Results header.
func (z *zoteroClient) get(ctx context.Context, path string, q url.Values) (json.RawMessage, int, int, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()

	u := fmt.Sprintf("%s/users/%s/%s?%s", cmp.Or(z.baseURL, "https://api.zotero.org"),
		url.PathEscape(z.userID), path, q.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, 0, err
	}
	req.Header.Set("Zotero-API-Version", "3")
	req.Header.Set("Zotero-API-Key", z.key)

	client := z.http
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, 0, err
	}
	defer resp.Body.Close()

	if wait := retryAfter(resp); wait > 0 && resp.StatusCode != http.StatusOK {
		return nil, 0, 0, &backoffError{wait: wait, msg: resp.Status}
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, 0, 0, fmt.Errorf("%s: %s", resp.Status, body)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, 0, err
	}
	version, _ := strconv.Atoi(resp.Header.Get("Last-Modified-Version"))
	total, err := strconv.Atoi(resp.Header.Get("Total-Results"))
	if err != nil {
		total = 0 // a single object, such as /deleted
	}
	// A Backoff header on a good response asks the next sync to wait.
	if wait := retryAfter(resp); wait > 0 {
		z.notBefore = time.Now().Add(wait)
	}
	return body, version, total, nil
}

// retryAfter reads the wait Zotero asks for, in seconds, from Backoff or
// Retry-After.
func retryAfter(resp *http.Response) time.Duration {
	for _, h := range []string{"Backoff", "Retry-After"} {
		if s, err := strconv.Atoi(resp.Header.Get(h)); err == nil && s > 0 {
			return time.Duration(s) * time.Second
		}
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable {
		return time.Minute
	}
	return 0
}

// isBackoff reports whether err asks to wait, and for how long.
func isBackoff(err error) (time.Duration, bool) {
	var b *backoffError
	if errors.As(err, &b) {
		return b.wait, true
	}
	return 0, false
}
