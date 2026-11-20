package mail

import (
	"sort"
	"strconv"
	"strings"
)

// parseMediaType tolerantly parses a Content-Type or Content-Disposition
// value (already converted to UTF-8, RFC 2047 words not decoded). It returns
// the lowercased type and lowercased-key parameters with RFC 2231
// continuations and charsets resolved. It never fails.
func parseMediaType(v string) (string, map[string]string) {
	v = strings.TrimSpace(v)
	typ := v
	rest := ""
	if i := strings.IndexByte(v, ';'); i >= 0 {
		typ, rest = v[:i], v[i+1:]
	}
	typ = strings.ToLower(strings.Join(strings.Fields(typ), ""))
	return typ, parseParams(rest)
}

type rawParam struct {
	key, value string
}

// parseParams parses "; k=v; k2="v2"" tolerantly. Unquoted values run to
// the next ';' (so unquoted names with spaces survive).
func parseParams(s string) map[string]string {
	var raws []rawParam
	i := 0
	for i < len(s) {
		for i < len(s) && (s[i] == ';' || s[i] == ' ' || s[i] == '\t') {
			i++
		}
		start := i
		for i < len(s) && s[i] != '=' && s[i] != ';' {
			i++
		}
		key := strings.ToLower(strings.TrimSpace(s[start:i]))
		if i >= len(s) || s[i] == ';' {
			continue // attribute without value
		}
		i++ // '='
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		var val strings.Builder
		if i < len(s) && s[i] == '"' {
			i++
			for i < len(s) && s[i] != '"' {
				if s[i] == '\\' && i+1 < len(s) {
					i++
				}
				val.WriteByte(s[i])
				i++
			}
			i++ // closing quote (or past end)
			for i < len(s) && s[i] != ';' {
				i++
			}
		} else {
			start := i
			for i < len(s) && s[i] != ';' {
				i++
			}
			val.WriteString(strings.TrimSpace(s[start:i]))
		}
		if key != "" {
			raws = append(raws, rawParam{key, val.String()})
		}
	}
	return resolveParams(raws)
}

// resolveParams applies RFC 2231: key*=charset'lang'pct, key*0*, key*1, ...
// Plain values get RFC 2047 decoding (common in names/filenames). Extended
// forms take precedence over plain ones.
func resolveParams(raws []rawParam) map[string]string {
	out := make(map[string]string)
	type seg struct {
		n       int
		value   string
		encoded bool
	}
	conts := map[string][]seg{}
	extended := map[string]string{}
	for _, p := range raws {
		k := p.key
		if !strings.Contains(k, "*") {
			if _, ok := out[k]; !ok {
				out[k] = decodeWords(p.value)
			}
			continue
		}
		parts := strings.Split(k, "*")
		base := parts[0]
		switch {
		case len(parts) == 2 && parts[1] == "": // key*
			extended[base] = decode2231(p.value)
		default: // key*N or key*N*
			n, err := strconv.Atoi(parts[1])
			if err != nil || n < 0 || n > 1000 {
				continue
			}
			enc := len(parts) == 3 && parts[2] == ""
			conts[base] = append(conts[base], seg{n, p.value, enc})
		}
	}
	for base, segs := range conts {
		sort.SliceStable(segs, func(i, j int) bool { return segs[i].n < segs[j].n })
		charset := ""
		var buf []byte
		for i, sg := range segs {
			v := sg.value
			if sg.encoded {
				if i == 0 {
					if cs, rest, ok := split2231(v); ok {
						charset, v = cs, rest
					}
				}
				buf = append(buf, pctDecode(v)...)
			} else {
				buf = append(buf, v...)
			}
		}
		extended[base] = DecodeCharset(buf, charset)
	}
	for k, v := range extended {
		out[k] = v
	}
	return out
}

// split2231 splits "charset'lang'rest".
func split2231(v string) (cs, rest string, ok bool) {
	i := strings.IndexByte(v, '\'')
	if i < 0 {
		return "", v, false
	}
	j := strings.IndexByte(v[i+1:], '\'')
	if j < 0 {
		return "", v, false
	}
	return v[:i], v[i+1+j+1:], true
}

func decode2231(v string) string {
	cs := ""
	if c, rest, ok := split2231(v); ok {
		cs, v = c, rest
	}
	return DecodeCharset(pctDecode(v), cs)
}

// pctDecode decodes %XX escapes, leaving malformed ones as is.
func pctDecode(s string) []byte {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) && isHex(s[i+1]) && isHex(s[i+2]) {
			out = append(out, unhex(s[i+1])<<4|unhex(s[i+2]))
			i += 2
			continue
		}
		out = append(out, s[i])
	}
	return out
}
