// Copyright 2025 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package ideas

import (
	"embed"
	"fmt"
	"strings"
	"text/template"
)

// Template names in prompts/.
const (
	promptAugment          = "augment.tmpl"
	promptTitle            = "title.tmpl"
	promptDetectTranslate  = "detect_translate.tmpl"
	promptSlug             = "slug.tmpl"
	promptTranslateTitle   = "translate_title.tmpl"
	promptTranslateContent = "translate_content.tmpl"
	promptIdeaInput        = "idea_input.tmpl"
	promptReference        = "reference.tmpl"
	promptTranslatePost    = "translate_post.tmpl"
)

//go:embed prompts/*.tmpl
var promptFS embed.FS

// prompts are parsed at startup; a template that does not parse panics.
// missingkey=error makes a missing map key an error instead of "<no value>".
var prompts = template.Must(
	template.New("prompts").Option("missingkey=error").ParseFS(promptFS, "prompts/*.tmpl"),
)

// augmentData is the data for augment.tmpl. Lang is the idea's language, "en"
// or "zh". WebTools is set when the request carries web search and fetch.
type augmentData struct {
	Lang     string
	WebTools bool
}

// languageData names a target language in English, such as "Chinese".
type languageData struct {
	Language string
}

// postData is the data for translate_post.tmpl: the post's language and
// the one it is translated into, by their English names.
type postData struct {
	From     string
	Language string
}

// ideaInputData is the data for idea_input.tmpl.
type ideaInputData struct {
	Title   string
	Content string
}

// referenceData is the data for reference.tmpl: a linked page and its text.
type referenceData struct {
	URL  string
	Text string
}

// renderPrompt executes the named template. It drops only the newline that
// ends the template file: idea_input.tmpl and reference.tmpl end with user
// content, whose trailing blank lines must survive.
func renderPrompt(name string, data any) (string, error) {
	var b strings.Builder
	if err := prompts.ExecuteTemplate(&b, name, data); err != nil {
		return "", fmt.Errorf("render prompt %s: %w", name, err)
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}

// languageName returns the English name of a language code.
func languageName(code string) string {
	if code == "zh" {
		return "Chinese"
	}
	return "English"
}
