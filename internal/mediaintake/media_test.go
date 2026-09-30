package mediaintake

import "testing"

func TestHasChineseSubtitle(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		// Hyphen delimiter -C- / -c-
		{name: "hyphen C middle with kanji", in: "JUQ-250-C-篠田ゆう", want: true},
		{name: "hyphen lowercase c middle with kanji", in: "JUQ-250-c-篠田ゆう", want: true},
		{name: "hyphen C middle", in: "JUQ-250-C-extra.mp4", want: true},
		// Dot delimiter .C. / .c.
		{name: "dot C middle with kanji", in: "JUQ-250.C.篠田ゆう", want: true},
		{name: "dot lowercase c middle with kanji", in: "JUQ-250.c.篠田ゆう", want: true},
		{name: "dot C middle tags", in: "JUQ-250.C.1080p.x265.mp4", want: true},
		// Underscore delimiter _C_ / _c_
		{name: "underscore C middle with kanji", in: "JUQ-250_C_篠田ゆう", want: true},
		{name: "underscore lowercase c middle with kanji", in: "JUQ-250_c_篠田ゆう", want: true},
		// Space delimiter
		{name: "space C middle with kanji", in: "JUQ-250 C 篠田ゆう", want: true},
		{name: "space lowercase c middle with kanji", in: "JUQ-250 c 篠田ゆう", want: true},
		// Bracket / parenthesis / symbols
		{name: "bracket C prefix", in: "[C] JUQ-250", want: true},
		{name: "bracket C middle", in: "JUQ-250 [C] 1080p", want: true},
		{name: "parenthesis C", in: "JUQ-250 (C)", want: true},
		{name: "pipe C middle with kanji", in: "JUQ-250|C|篠田ゆう", want: true},
		{name: "pipe lowercase c middle with kanji", in: "JUQ-250|c|篠田ゆう", want: true},
		{name: "question mark C middle with kanji", in: "JUQ-250?C?篠田ゆう", want: true},
		{name: "question mark lowercase c middle with kanji", in: "JUQ-250?c?篠田ゆう", want: true},
		{name: "pipe UC middle with kanji", in: "JUQ-250|UC|篠田ゆう", want: true},
		{name: "question mark UC middle with kanji", in: "JUQ-250?UC?篠田ゆう", want: true},
		{name: "exclamation C middle with kanji", in: "JUQ-250!C!篠田ゆう", want: true},
		{name: "tilde C middle with kanji", in: "JUQ-250~C~篠田ゆう", want: true},
		{name: "plus C middle with kanji", in: "JUQ-250+C+篠田ゆう", want: true},
		{name: "at C middle with kanji", in: "JUQ-250@C@篠田ゆう", want: true},
		{name: "pipe C end of string", in: "JUQ-250|C", want: true},
		{name: "question mark C end of string", in: "JUQ-250?C", want: true},
		{name: "pipe UC end of string", in: "JUQ-250|UC", want: true},
		{name: "question mark UC end of string", in: "JUQ-250?UC", want: true},
		// End of filename (before extension)
		{name: "hyphen C end of filename", in: "JUQ-250-C.mp4", want: true},
		{name: "dot c end of filename", in: "JUQ-250.c.mkv", want: true},
		{name: "underscore C end of filename", in: "JUQ-250_C.avi", want: true},
		{name: "space C end of filename", in: "JUQ-250 C.mp4", want: true},
		// End of string (folder or torrent name)
		{name: "hyphen C end of string", in: "JUQ-250-C", want: true},
		{name: "dot c end of string", in: "JUQ-250.c", want: true},
		{name: "underscore C end of string", in: "JUQ-250_C", want: true},
		{name: "space C end of string", in: "JUQ-250 C", want: true},
		// UC tags - case-insensitive, middle and end
		{name: "hyphen UC middle with kanji", in: "JUQ-250-UC-篠田ゆう", want: true},
		{name: "hyphen lowercase uc middle with kanji", in: "JUQ-250-uc-篠田ゆう", want: true},
		{name: "hyphen mixed Uc middle with kanji", in: "JUQ-250-Uc-篠田ゆう", want: true},
		{name: "dot UC middle tags", in: "JUQ-250.UC.1080p.mp4", want: true},
		{name: "dot lowercase uc middle tags", in: "JUQ-250.uc.720p.mp4", want: true},
		{name: "underscore UC middle with kanji", in: "JUQ-250_UC_篠田ゆう", want: true},
		{name: "space UC middle with kanji", in: "JUQ-250 UC 篠田ゆう", want: true},
		{name: "hyphen UC end of filename", in: "JUQ-250-UC.mp4", want: true},
		{name: "dot uc end of filename", in: "JUQ-250.uc.mkv", want: true},
		{name: "underscore UC end of filename", in: "JUQ-250_UC.avi", want: true},
		{name: "hyphen UC end of string", in: "JUQ-250-UC", want: true},
		{name: "dot uc end of string", in: "JUQ-250.uc", want: true},
		{name: "underscore UC end of string", in: "JUQ-250_UC", want: true},
		{name: "bracket UC", in: "[UC] JUQ-250", want: true},
		// Fullwidth characters
		{name: "fullwidth C middle with kanji", in: "JUQ-250-Ｃ-篠田ゆう", want: true},
		{name: "fullwidth UC middle with kanji", in: "JUQ-250-ＵＣ-篠田ゆう", want: true},
		// Directly followed by CJK without trailing delimiter
		{name: "hyphen C followed by kanji", in: "JUQ-250-C篠田ゆう", want: true},
		{name: "hyphen UC followed by kanji", in: "JUQ-250-UC篠田ゆう", want: true},
		// Language codes and Chinese terms
		{name: "chs language code", in: "JUR-456.chs.mp4", want: true},
		{name: "cht language code", in: "STARS-123.cht.mp4", want: true},
		{name: "chinese terms", in: "STARS-123.中文字幕.mp4", want: true},
		{name: "sc code", in: "ABC-123.sc.mp4", want: true},
		{name: "tc code", in: "ABC-123.tc.mp4", want: true},

		// Negative cases
		{name: "plain video without subtitle", in: "JUQ-250.mp4", want: false},
		{name: "plain video with kanji only", in: "JUQ-250-篠田ゆう.mp4", want: false},
		{name: "CD1 part marker", in: "JUQ-250-CD1.mp4", want: false},
		{name: "CD2 part marker", in: "JUQ-250-CD2.mp4", want: false},
		{name: "CH1 marker", in: "JUQ-250-CH1.mp4", want: false},
		{name: "UHD resolution tag", in: "JUQ-250-UHD.mp4", want: false},
		{name: "UNCUT tag", in: "JUQ-250-UNCUT.mp4", want: false},
		{name: "UNCENSORED tag", in: "JUQ-250-UNCENSORED.mp4", want: false},
		{name: "HEVC codec tag", in: "JUQ-250-HEVC.mp4", want: false},
		{name: "AVC codec tag", in: "JUQ-250-AVC.mp4", want: false},
		{name: "AAC audio tag", in: "JUQ-250-AAC.mp4", want: false},
		{name: "AC3 audio tag", in: "JUQ-250-AC3.mp4", want: false},
		{name: "missing delimiter attached C", in: "JUQ-250C.mp4", want: false},
		{name: "missing delimiter attached UC", in: "JUQ-250UC.mp4", want: false},
		{name: "English word music", in: "music.mp4", want: false},
		{name: "English word comic", in: "comic.mp4", want: false},
		{name: "English word clean", in: "clean.mp4", want: false},
		{name: "English word classic", in: "classic.mp4", want: false},
		{name: "code with C inside", in: "FC2-PPV-123456.mp4", want: false},
		{name: "code starting with RCTD", in: "RCTD-123.mp4", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := HasChineseSubtitle(tt.in)
			if got != tt.want {
				t.Fatalf("HasChineseSubtitle(%q) = %v; want %v", tt.in, got, tt.want)
			}
		})
	}
}
