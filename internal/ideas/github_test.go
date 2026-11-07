// Copyright 2025 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package ideas

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestSanitizeCommitMsg checks that control characters and surrounding space
// are dropped, and everything else is kept.
func TestSanitizeCommitMsg(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain text is untouched", in: "ideas: A Short Title", want: "ideas: A Short Title"},
		{name: "control characters are dropped", in: "ideas: a\x00b\nc\td", want: "ideas: abcd"},
		{name: "surrounding space is trimmed", in: "  ideas: title  ", want: "ideas: title"},
		{name: "chinese is preserved", in: "ideas: 中文标题", want: "ideas: 中文标题"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeCommitMsg(tt.in); got != tt.want {
				t.Fatalf("sanitizeCommitMsg(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestSanitizeCommitMsgTruncatesOnRunes checks that a long Chinese subject is
// cut on a character boundary.
func TestSanitizeCommitMsgTruncatesOnRunes(t *testing.T) {
	long := "ideas: " + strings.Repeat("标", 300)

	got := sanitizeCommitMsg(long)
	if !utf8.ValidString(got) {
		t.Fatalf("sanitizeCommitMsg() produced invalid UTF-8: %q", got)
	}
	// The cut is marked with one ellipsis.
	if n := utf8.RuneCountInString(got); n != commitMsgMaxRunes+1 {
		t.Fatalf("rune count = %d, want %d", n, commitMsgMaxRunes+1)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("sanitizeCommitMsg() = %q, want a trailing ellipsis to mark the cut", got)
	}
}
