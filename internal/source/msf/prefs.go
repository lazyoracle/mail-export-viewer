package msf

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/lazyoracle/mail-export-viewer/internal/source"
)

const (
	maxPrefsBytes = 16 << 20
	maxPrefsDepth = 16 // directories to climb looking for prefs.js
)

// defaultTags are Thunderbird's built-in labels.
var defaultTags = []source.TagStyle{
	{Name: "Important", Color: "#FF0000"}, {Name: "Work", Color: "#FF9900"},
	{Name: "Personal", Color: "#009900"}, {Name: "To Do", Color: "#3333FF"},
	{Name: "Later", Color: "#993399"},
}

// findPrefs walks up from dir to the nearest prefs.js (the profile root)
// and returns the mailnews.tags.* preferences found in it, keyed
// "<key>.tag" / "<key>.color", and its path ("" when there is none).
func findPrefs(dir string) (map[string]string, string) {
	for i := 0; i < maxPrefsDepth; i++ {
		p := filepath.Join(dir, "prefs.js")
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() && st.Size() <= maxPrefsBytes {
			if data, err := os.ReadFile(p); err == nil {
				return parsePrefs(string(data)), p
			}
			return nil, ""
		}
		up := filepath.Dir(dir)
		if up == dir {
			break
		}
		dir = up
	}
	return nil, ""
}

// parsePrefs extracts string-valued user_pref("mailnews.tags.<key>.<tag|color>", "...").
func parsePrefs(s string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		rest, ok := strings.CutPrefix(line, `user_pref("mailnews.tags.`)
		if !ok {
			continue
		}
		name, rest, ok := strings.Cut(rest, `",`)
		if !ok || !(strings.HasSuffix(name, ".tag") || strings.HasSuffix(name, ".color")) {
			continue
		}
		rest = strings.TrimSpace(rest)
		if !strings.HasPrefix(rest, `"`) {
			continue
		}
		if v, ok := jsString(rest[1:]); ok {
			out[name] = v
		}
	}
	return out
}

// jsString decodes a JavaScript string literal body up to its closing quote.
func jsString(s string) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '"':
			return b.String(), true
		case '\\':
			i++
			if i >= len(s) {
				return "", false
			}
			switch e := s[i]; e {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case 'x', 'u':
				n := 2
				if e == 'u' {
					n = 4
				}
				if i+n >= len(s) {
					return "", false
				}
				v, err := strconv.ParseUint(s[i+1:i+1+n], 16, 32)
				if err != nil {
					return "", false
				}
				b.WriteRune(rune(v))
				i += n
			default:
				b.WriteByte(e)
			}
		default:
			b.WriteByte(c)
		}
	}
	return "", false
}

// tagStyles builds the TagInfo map: the default $label1..5, overridden and
// extended by the profile's mailnews.tags.* preferences.
func tagStyles(prefs map[string]string) map[string]source.TagStyle {
	out := map[string]source.TagStyle{}
	for i, t := range defaultTags {
		out["$label"+strconv.Itoa(i+1)] = t
	}
	for name, v := range prefs {
		if !utf8.ValidString(v) {
			continue
		}
		if key, ok := strings.CutSuffix(name, ".tag"); ok && v != "" {
			st := out[key]
			st.Name = v
			out[key] = st
		}
	}
	for name, v := range prefs {
		if key, ok := strings.CutSuffix(name, ".color"); ok {
			if st, named := out[key]; named {
				st.Color = v
				out[key] = st
			}
		}
	}
	return out
}
