// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package library

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"go.yaml.in/yaml/v3"
)

// Share is a set of topics shared with named members, and optionally with
// anyone, read-only.
type Share struct {
	ID      string    `yaml:"id"`
	Title   string    `yaml:"title"`
	Topics  []string  `yaml:"topics"` // collection keys; subtopics included
	Public  bool      `yaml:"public"` // also readable at /library/p/<id>
	Members []*Member `yaml:"members"`
}

// Member is one person's link to a share. The token is the whole credential.
type Member struct {
	Name    string `yaml:"name"`
	Token   string `yaml:"token"`
	Revoked string `yaml:"revoked,omitempty"` // a date; the link then answers 404
}

// Post is one line of a share's .jsonl: a comment, a suggestion, or the owner
// hiding one of them.
type Post struct {
	ID     string    `json:"id"`
	Time   time.Time `json:"t"`
	By     string    `json:"by"`
	Type   string    `json:"type"`             // comment, suggest, or hide
	Entry  string    `json:"entry,omitempty"`  // comment: item key, or "" for the share
	Text   string    `json:"text,omitempty"`   // comment
	Ref    string    `json:"ref,omitempty"`    // suggest: DOI, arXiv ID, URL, or BibTeX
	Note   string    `json:"note,omitempty"`   // suggest
	Target string    `json:"target,omitempty"` // hide: the post id

	Share string `json:"-"` // the share it belongs to
}

// store is the state/ folder: the only data that exists nowhere else.
//
//	state/shares.yaml   every share
//	state/<id>.jsonl    one per share, append-only
//	state/read.txt      ids of posts the owner marked read
//	state/session.key   signs the owner's session cookie
type store struct {
	dir string

	mu     sync.Mutex
	shares []*Share
	posts  map[string][]*Post // by share id, oldest first, hide records included
	read   map[string]bool
	key    []byte
}

// openStore loads dir, creating it and the session key if missing.
func openStore(dir string) (*store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &store{dir: dir, posts: map[string][]*Post{}, read: map[string]bool{}}

	raw, err := os.ReadFile(filepath.Join(dir, "shares.yaml"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if err := yaml.Unmarshal(raw, &s.shares); err != nil {
		return nil, fmt.Errorf("shares.yaml: %w", err)
	}
	for _, sh := range s.shares {
		posts, err := readPosts(filepath.Join(dir, sh.ID+".jsonl"))
		if err != nil {
			return nil, err
		}
		for _, p := range posts {
			p.Share = sh.ID
		}
		s.posts[sh.ID] = posts
	}

	raw, err = os.ReadFile(filepath.Join(dir, "read.txt"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	for id := range strings.FieldsSeq(string(raw)) {
		s.read[id] = true
	}

	keyPath := filepath.Join(dir, "session.key")
	raw, err = os.ReadFile(keyPath)
	switch {
	case err == nil:
		s.key, err = hex.DecodeString(strings.TrimSpace(string(raw)))
		if err != nil || len(s.key) < 32 {
			return nil, fmt.Errorf("session.key is not 32 hex-encoded bytes")
		}
	case errors.Is(err, fs.ErrNotExist):
		s.key = make([]byte, 32)
		rand.Read(s.key)
		if err := writeFile(keyPath, []byte(hex.EncodeToString(s.key)+"\n")); err != nil {
			return nil, err
		}
	default:
		return nil, err
	}
	return s, nil
}

// readPosts reads a .jsonl file. A line that does not parse, such as one cut
// off by a crash, is skipped.
func readPosts(path string) ([]*Post, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []*Post
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var p Post
		if json.Unmarshal(sc.Bytes(), &p) == nil && p.ID != "" {
			out = append(out, &p)
		}
	}
	return out, sc.Err()
}

// writeFile writes data to a temporary file and renames it over path, so a
// crash leaves the old file or the new one, never half of one.
func writeFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// saveShares writes shares.yaml. The caller holds s.mu.
func (s *store) saveShares() error {
	var b bytes.Buffer
	b.WriteString("# Shares of changkun.de/library. Written by the server; edit it only while\n# the server is stopped.\n")
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(s.shares); err != nil {
		return err
	}
	return writeFile(filepath.Join(s.dir, "shares.yaml"), b.Bytes())
}

// randomID returns n random bytes in lowercase base32, without padding.
func randomID(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))
}

// createShare adds a share with one member per name and returns it.
func (s *store) createShare(title string, topics, names []string, public bool) (*Share, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sh := &Share{Title: title, Topics: topics, Public: public}
	for sh.ID == "" || s.share(sh.ID) != nil {
		sh.ID = randomID(3) // 5 characters
	}
	for _, n := range names {
		sh.Members = append(sh.Members, &Member{Name: n, Token: randomID(16)}) // 128 bits
	}
	s.shares = append(s.shares, sh)
	if err := s.saveShares(); err != nil {
		s.shares = s.shares[:len(s.shares)-1]
		return nil, err
	}
	return sh, nil
}

// addMembers adds one member per name to share id.
func (s *store) addMembers(id string, names []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sh := s.share(id)
	if sh == nil {
		return fs.ErrNotExist
	}
	n := len(sh.Members)
	for _, name := range names {
		sh.Members = append(sh.Members, &Member{Name: name, Token: randomID(16)})
	}
	if err := s.saveShares(); err != nil {
		sh.Members = sh.Members[:n]
		return err
	}
	return nil
}

// updateShare sets a share's title, topics, and public flag.
func (s *store) updateShare(id, title string, topics []string, public bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sh := s.share(id)
	if sh == nil {
		return fs.ErrNotExist
	}
	old := *sh
	sh.Title, sh.Topics, sh.Public = title, topics, public
	if err := s.saveShares(); err != nil {
		*sh = old
		return err
	}
	return nil
}

// revoke marks the member with token as revoked today.
func (s *store) revoke(id, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sh := s.share(id)
	if sh == nil {
		return fs.ErrNotExist
	}
	for _, m := range sh.Members {
		if m.Token == token && m.Revoked == "" {
			m.Revoked = time.Now().UTC().Format(time.DateOnly)
			if err := s.saveShares(); err != nil {
				m.Revoked = ""
				return err
			}
			return nil
		}
	}
	return fs.ErrNotExist
}

// share returns share id, or nil. The caller holds s.mu.
func (s *store) share(id string) *Share {
	for _, sh := range s.shares {
		if sh.ID == id {
			return sh
		}
	}
	return nil
}

// shareByID returns a copy of share id, or nil.
func (s *store) shareByID(id string) *Share {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sh := s.share(id); sh != nil {
		return copyShare(sh)
	}
	return nil
}

// member returns copies of the share and the unrevoked member that token
// belongs to, or nils.
func (s *store) member(token string) (*Share, *Member) {
	if token == "" {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sh := range s.shares {
		for _, m := range sh.Members {
			if m.Token == token && m.Revoked == "" {
				c := *m
				return copyShare(sh), &c
			}
		}
	}
	return nil, nil
}

// allShares returns copies of every share, newest first.
func (s *store) allShares() []*Share {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Share, 0, len(s.shares))
	for _, sh := range slices.Backward(s.shares) {
		out = append(out, copyShare(sh))
	}
	return out
}

func copyShare(sh *Share) *Share {
	c := *sh
	c.Topics = slices.Clone(sh.Topics)
	c.Members = make([]*Member, len(sh.Members))
	for i, m := range sh.Members {
		mc := *m
		c.Members[i] = &mc
	}
	return &c
}

// errLimit is returned when a member has posted too much today.
var errLimit = errors.New("daily limit reached")

// postsPerDay is how many posts one member may make in 24 hours.
const postsPerDay = 30

// appendPost appends p to its share's .jsonl and returns it with its id and
// time set. The line is synced to disk before it returns.
func (s *store) appendPost(shareID string, p Post) (*Post, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.share(shareID) == nil {
		return nil, fs.ErrNotExist
	}
	now := time.Now().UTC()
	if p.Type != "hide" {
		n := 0
		for _, q := range s.posts[shareID] {
			if q.By == p.By && q.Type != "hide" && now.Sub(q.Time) < 24*time.Hour {
				n++
			}
		}
		if n >= postsPerDay {
			return nil, errLimit
		}
	}
	p.ID = randomID(5)
	p.Time = now.Truncate(time.Second)
	p.Share = shareID
	line, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(s.dir, shareID+".jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	s.posts[shareID] = append(s.posts[shareID], &p)
	return &p, nil
}

// visiblePosts returns the comments and suggestions of share id that are not
// hidden, oldest first. An empty id returns those of every share.
func (s *store) visiblePosts(id string) []*Post {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*Post
	for _, sh := range s.shares {
		if id != "" && sh.ID != id {
			continue
		}
		hidden := map[string]bool{}
		for _, p := range s.posts[sh.ID] {
			if p.Type == "hide" {
				hidden[p.Target] = true
			}
		}
		for _, p := range s.posts[sh.ID] {
			if p.Type != "hide" && !hidden[p.ID] {
				c := *p
				out = append(out, &c)
			}
		}
	}
	slices.SortStableFunc(out, func(a, b *Post) int { return a.Time.Compare(b.Time) })
	return out
}

// isRead reports whether the owner marked post id read.
func (s *store) isRead(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.read[id]
}

// markRead appends ids to read.txt.
func (s *store) markRead(ids []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var b strings.Builder
	for _, id := range ids {
		if id != "" && !s.read[id] {
			b.WriteString(id + "\n")
		}
	}
	if b.Len() == 0 {
		return nil
	}
	f, err := os.OpenFile(filepath.Join(s.dir, "read.txt"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(b.String()); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	for _, id := range ids {
		s.read[id] = true
	}
	return nil
}

// sessionKey returns the key that signs owner cookies.
func (s *store) sessionKey() []byte {
	return s.key
}
