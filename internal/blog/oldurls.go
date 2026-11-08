package blog

import (
	"fmt"
	"os"
	"path"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// The Hugo blog wrote some pages and files at more than one address over
// the years, and the folder the server published kept every one of them:
// a post at its file's name as well as at its slug, the images of early
// posts under images/posts/<id>/ before they moved into the posts'
// bundles, and the pages of a list as p/<n>/ before page/<n>/. Links and
// search results still point at them, so oldURL sends each on, for good,
// to what it was.

// addOldURLs indexes the pages of the content folder dir by the addresses
// of their files' names and by the ids of their front matter.
func (s *site) addOldURLs(dir string) {
	s.byID = make(map[string]*Page)
	for _, p := range s.pages {
		if p.Section != "" {
			a := "/" + p.Section + "/" + p.BaseName + "/"
			if _, ok := s.byPath[a]; !ok {
				if _, ok := s.aliases[a]; !ok {
					s.aliases[a] = p.Path
				}
			}
		}
		if id := pageID(fileOf(dir, p.File)); id != "" && s.byID[id] == nil {
			s.byID[id] = p
		}
	}
}

// pageID returns the id of the front matter of the page in file name, or
// "" if it has none.
func pageID(name string) string {
	src, err := os.ReadFile(name)
	if err != nil {
		return ""
	}
	fm, _ := splitFrontMatter(src)
	var v struct {
		ID any `yaml:"id"`
	}
	if fm == nil || yaml.Unmarshal(fm, &v) != nil || v.ID == nil {
		return ""
	}
	return fmt.Sprint(v.ID)
}

// oldURL returns the address that the old address at, below the blog, now
// has, if it has one.
func (s *site) oldURL(at string) (string, bool) {
	// An early post's image, now in the post's bundle: the folder is the
	// post's id, or an alias of the post when it had none. Hugo wrote a
	// few with a second extension, 1.jpeg.jpg for 1.jpeg.
	if rest, ok := strings.CutPrefix(at, "/images/posts/"); ok {
		id, name, _ := strings.Cut(rest, "/")
		p := s.byID[id]
		if p == nil {
			p = s.byPath[s.aliases["/images/posts/"+id+"/"]]
		}
		if p != nil && name != "" {
			for _, n := range []string{name, strings.TrimSuffix(name, path.Ext(name))} {
				if _, ok := s.files[p.Path+n]; ok && n != "" && !strings.HasSuffix(n, "/") {
					return blogURL(p.Path + n), true
				}
			}
		}
		return "", false
	}
	if at == "/images/logo.png" {
		return "/logo.png", true
	}
	if !strings.HasSuffix(at, "/") {
		return "", false
	}
	// A page of a list as Hugo first numbered them, p/<n>/, its first
	// page as page/1/, or a page past the end of a list that is shorter
	// now: the same page, or the list itself.
	for _, seg := range []string{"/p/", "/page/"} {
		i := strings.LastIndex(at, seg)
		if i < 0 {
			continue
		}
		list, num := at[:i+1], strings.TrimSuffix(at[i+len(seg):], "/")
		n, err := strconv.Atoi(num)
		if err != nil || n < 1 || strings.Contains(num, "/") || s.response(list) == nil {
			continue
		}
		if to := list + "page/" + num + "/"; n > 1 && to != at && s.response(to) != nil {
			return blogURL(to), true
		}
		return blogURL(list), true
	}
	// A tag that no published page has any more, and Hugo's categories,
	// which the blog never used: the tags.
	if rest, ok := strings.CutPrefix(at, "/tags/"); ok {
		if key, _, _ := strings.Cut(rest, "/"); s.tagsBy[key] == nil {
			return blogURL("/tags/"), true
		}
	}
	if at == "/categories/" {
		return blogURL("/tags/"), true
	}
	return "", false
}

// blogURL returns the address of p, an address below the blog.
func blogURL(p string) string { return Prefix + strings.TrimPrefix(p, "/") }
