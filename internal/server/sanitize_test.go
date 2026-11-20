package server

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"
)

func TestSanitizeHTML(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		want      []string // substrings that must appear
		reject    []string // substrings that must not appear (case-insensitive)
		hasRemote bool
	}{
		{name: "plain markup kept", in: `<p class="x">Hello <b>world</b></p>`,
			want: []string{`<p class="x">Hello <b>world</b></p>`}},
		{name: "script dropped with content", in: `<p>a</p><script>alert(1)</script><p>b</p>`,
			want: []string{"<p>a</p>", "<p>b</p>"}, reject: []string{"<script", "alert"}},
		{name: "odd-case script", in: `<ScRiPt>alert(1)</sCrIpT>ok`,
			want: []string{"ok"}, reject: []string{"<script", "alert"}},
		{name: "unclosed script swallows rest", in: `x<script>alert(1)`,
			want: []string{"x"}, reject: []string{"<script", "alert"}},
		{name: "iframe object embed applet", in: `<iframe src="http://e.vil/"></iframe><object data="x.swf"><param name=a></object><embed src="x"><applet code=x></applet>t`,
			want: []string{"t"}, reject: []string{"<iframe", "<object", "<embed", "<applet", "<param", "e.vil"}},
		{name: "img onerror", in: `<img src=x onerror="alert(1)">`,
			reject: []string{"onerror", "alert", `src=`}},
		{name: "on* attributes", in: `<div onclick="a()" OnMouseOver="b()" style="color:red">t</div>`,
			want: []string{`<div style="color:red">t</div>`}, reject: []string{"onclick", "onmouseover"}},
		{name: "javascript href", in: `<a href="javascript:alert(1)">x</a>`,
			want: []string{"<a>x</a>"}, reject: []string{"javascript"}},
		{name: "odd-case javascript href", in: `<a href="JaVaScRiPt:alert(1)">x</a>`,
			reject: []string{"javascript", "href"}},
		{name: "entity-encoded javascript href", in: `<a href="java&#x73;cript:alert(1)">x</a>`,
			reject: []string{"javascript", "href", "alert"}},
		{name: "tab-split javascript href", in: "<a href=\"java\tscript:alert(1)\">x</a>",
			reject: []string{"script:", "href"}},
		{name: "leading space javascript", in: `<a href="  javascript:alert(1)">x</a>`,
			reject: []string{"javascript", "href"}},
		{name: "vbscript href", in: `<a href="vbscript:msgbox(1)">x</a>`, reject: []string{"vbscript", "href"}},
		{name: "data text/html href", in: `<a href="data:text/html,<script>alert(1)</script>">x</a>`,
			reject: []string{"data:", "href", "<script"}},
		{name: "http link gets target and rel", in: `<a href="https://example.com/a?b=1&amp;c=2" target="_self" rel="opener">x</a>`,
			want:   []string{`href="https://example.com/a?b=1&amp;c=2"`, `target="_blank"`, `rel="noopener noreferrer"`},
			reject: []string{"_self", `"opener"`}},
		{name: "mailto kept", in: `<a href="mailto:a@b.c">m</a>`, want: []string{`href="mailto:a@b.c"`}},
		{name: "fragment link kept", in: `<a href="#sec">s</a>`, want: []string{`href="#sec"`}},
		{name: "relative link dropped", in: `<a href="/api/messages/1/raw">r</a>`, reject: []string{"href"}},
		{name: "remote img blocked", in: `<img src="http://tracker.example/p.gif" alt="a">`,
			want: []string{`data-blocked-src="http://tracker.example/p.gif"`, `alt="a"`}, reject: []string{" src="},
			hasRemote: true},
		{name: "protocol-relative img blocked", in: `<img src="//tracker.example/p.gif">`,
			want: []string{`data-blocked-src="//tracker.example/p.gif"`}, reject: []string{" src="}, hasRemote: true},
		{name: "data png img kept", in: `<img src="data:image/png;base64,iVBORw0KGgo=">`,
			want: []string{`src="data:image/png;base64,iVBORw0KGgo="`}},
		{name: "data svg img dropped", in: `<img src="data:image/svg+xml;base64,PHN2Zz4=">`,
			reject: []string{"data:", "src="}},
		{name: "cid img rewritten", in: `<img src="cid:logo@x.y">`,
			want: []string{`src="/api/messages/42/cid/logo@x.y"`}},
		{name: "cid img percent-encoded and slash", in: `<img src="CID:a%2Fb%20c">`,
			want: []string{`src="/api/messages/42/cid/a%2Fb%20c"`}},
		{name: "srcset dropped", in: `<img src="cid:a" srcset="http://evil.example/x.png 2x">`,
			want: []string{`src="/api/messages/42/cid/a"`}, reject: []string{"srcset", "evil"}, hasRemote: true},
		{name: "css url remote blocked", in: `<div style="background:url(http://t.example/x.png) no-repeat; color: red">t</div>`,
			want: []string{"color: red"}, reject: []string{"t.example", "url("}, hasRemote: true},
		{name: "css url quoted remote blocked", in: `<div style='background-image: URL( "https://t.example/x.png" )'>t</div>`,
			reject: []string{"t.example", "url("}, hasRemote: true},
		{name: "css url cid rewritten", in: `<td style="background:url('cid:bg')">t</td>`,
			want: []string{`url(&#34;/api/messages/42/cid/bg&#34;)`}},
		{name: "css expression", in: `<div style="width: expression(alert(1))">t</div>`,
			reject: []string{"expression", "alert", "style="}},
		{name: "css escaped expression", in: `<div style="width: ex\70 ression(alert(1))">t</div>`,
			reject: []string{"ression", "alert"}},
		{name: "css comment split expression", in: `<div style="width: expr/**/ession(alert(1))">t</div>`,
			reject: []string{"ession", "alert"}},
		{name: "css javascript url", in: `<div style="background:url(javascript:alert(1))">t</div>`,
			reject: []string{"javascript", "alert"}},
		{name: "css moz-binding", in: `<div style="-moz-binding:url(http://x/y.xml#z)">t</div>`,
			reject: []string{"binding", "x/y"}, hasRemote: true},
		{name: "css splice import into url attr", in: `<div style="background:u@import x;rl(http://evil/a.png)">t</div>`,
			reject: []string{"url(", "evil", "@import"}, hasRemote: true},
		{name: "css splice import into import", in: `<style>@imp@import ;ort "http://evil/x.css"; body{}</style>`,
			want: []string{"body{}"}, reject: []string{"@import", "url("}},
		{name: "css splice protocol-relative attr", in: `<div style="background:u@import;rl(//evil/p)">t</div>`,
			reject: []string{"url(", "evil", "@import"}, hasRemote: true},
		{name: "css splice protocol-relative style", in: `<style>p{background:u@import;rl(//evil/p)}</style>`,
			reject: []string{"url(", "@import"}},
		{name: "css splice entity attr", in: `<div style="background:u&#x40;import;rl(http://evil/a.png)">t</div>`,
			reject: []string{"url(", "evil", "@import"}, hasRemote: true},
		{name: "css splice entity style", in: `<style>p{background:u&#x40;import;rl(http://evil/a.png)}</style>`,
			reject: []string{"url(", "@import"}},
		{name: "css splice url into expression", in: `<div style="width:expressio url(x)n(alert(1))">t</div>`,
			reject: []string{"expression", "url("}},
		{name: "cid dot segments rejected", in: `<img src="cid:.."><img src="cid:%2e"><img src="cid:%2E%2E">`,
			reject: []string{"src=", "/cid/"}},
		{name: "style element import dropped", in: `<style>@import url(http://evil.example/a.css); p{color:red}</style><p>t</p>`,
			want: []string{"<style>", "p{color:red}", "<p>t</p>"}, reject: []string{"@import", "evil"}, hasRemote: true},
		{name: "style element import string", in: `<style>@IMPORT "http://evil.example/a.css";</style>`,
			reject: []string{"import", "evil"}, hasRemote: true},
		{name: "style element remote url", in: `<style>body{background:url(https://evil.example/b.png)}</style>`,
			want: []string{"<style>body{background: none }</style>"}, reject: []string{"evil"}, hasRemote: true},
		{name: "style element cannot break out", in: `<style>p{}</style ><script>alert(1)</script>`,
			reject: []string{"<script", "alert"}},
		{name: "style element with lt", in: "<style>p{content:'</sty' 'le><script>x</script>'}</style>",
			reject: []string{"<script", "</sty'"}},
		{name: "meta refresh", in: `<meta http-equiv="refresh" content="0;url=http://evil.example/">t`,
			want: []string{"t"}, reject: []string{"<meta", "refresh", "evil"}},
		{name: "base href", in: `<base href="http://evil.example/"><a href="x">l</a>`,
			reject: []string{"<base", "evil", "href"}},
		{name: "link stylesheet", in: `<link rel="stylesheet" href="http://evil.example/a.css">t`,
			reject: []string{"<link", "evil"}},
		{name: "form action", in: `<form action="http://evil.example/" method="post"><input name="pw" type="password"><button formaction="http://evil.example/">go</button><textarea>secret</textarea><select><option>o</option></select></form>`,
			want: []string{"go"}, reject: []string{"<form", "action", "<input", "<button", "<textarea", "secret", "<select", "evil"}},
		{name: "nested svg with script", in: `<svg><g><script>alert(1)</script><a xlink:href="javascript:alert(2)"><text>x</text></a></g></svg>after`,
			want: []string{"after"}, reject: []string{"<svg", "<script", "alert", "javascript"}},
		{name: "math dropped", in: `<math><mtext><table><mglyph><style><img src=x onerror=alert(1)>`,
			reject: []string{"onerror", "alert", "<math"}},
		{name: "noscript dropped", in: `<noscript><p title="</noscript><img src=x onerror=alert(1)>"></noscript>z`,
			reject: []string{"onerror", "alert"}},
		{name: "uppercase tags normalised", in: `<DIV ALIGN="center"><B>Bold</B></DIV>`,
			want: []string{`<div align="center"><b>Bold</b></div>`}},
		{name: "unclosed tags closed", in: `<div><table><tr><td>x`,
			want: []string{"<div><table><tr><td>x</td></tr></table></div>"}},
		{name: "stray end tags dropped", in: `</div></table>x</p>`, want: []string{"x"}, reject: []string{"</div>", "</table>"}},
		{name: "comments dropped", in: `a<!--[if IE]><script>alert(1)</script><![endif]-->b`,
			want: []string{"ab"}, reject: []string{"<!--", "alert"}},
		{name: "text escaped", in: `1 &lt; 2 &amp; <b>x</b>`, want: []string{"1 &lt; 2 &amp; <b>x</b>"}},
		{name: "head title dropped body kept", in: `<html><head><title>T</title></head><body><p>b</p></body></html>`,
			want: []string{"<p>b</p>"}, reject: []string{"<title", "T</", "<body", "<html"}},
		{name: "background attr remote", in: `<table background="http://t.example/bg.png"><tr><td>x</td></tr></table>`,
			reject: []string{"background", "t.example"}, hasRemote: true},
		{name: "background attr cid", in: `<td background="cid:bg">x</td>`,
			want: []string{`background="/api/messages/42/cid/bg"`}},
		{name: "video poster dropped", in: `<video poster="http://t.example/p.png"><source src="http://t.example/v.mp4"></video>x`,
			want: []string{"x"}, reject: []string{"<video", "t.example"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, remote := sanitizeHTML(c.in, 42)
			for _, w := range c.want {
				if !strings.Contains(out, w) {
					t.Errorf("output missing %q\n in: %s\nout: %s", w, c.in, out)
				}
			}
			lo := strings.ToLower(out)
			for _, r := range c.reject {
				if strings.Contains(lo, strings.ToLower(r)) {
					t.Errorf("output contains %q\n in: %s\nout: %s", r, c.in, out)
				}
			}
			if remote != c.hasRemote {
				t.Errorf("hasRemote = %v, want %v\nout: %s", remote, c.hasRemote, out)
			}
			assertSafeHTML(t, out)
			assertSafeCSS(t, out, 42)
		})
	}
}

// urlAttrs are the attributes whose values a browser may treat as a URL.
var urlAttrs = map[string]bool{"href": true, "src": true, "action": true, "formaction": true,
	"background": true, "data": true, "poster": true, "cite": true, "srcset": true,
	"xlink:href": true, "longdesc": true, "lowsrc": true, "dynsrc": true, "style": true, "codebase": true}

// assertSafeHTML parses out the way a browser would and fails if it contains
// a script element, an event-handler attribute or a javascript: URL.
func assertSafeHTML(t testing.TB, out string) {
	t.Helper()
	if strings.Contains(strings.ToLower(out), "<script") {
		t.Fatalf("output contains <script: %q", out)
	}
	doc, err := html.Parse(strings.NewReader(out))
	if err != nil {
		t.Fatalf("parse output: %v", err)
	}
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "script", "iframe", "object", "embed", "form", "input", "base", "meta", "link", "svg", "math":
				t.Fatalf("output contains <%s>: %q", n.Data, out)
			}
			for _, a := range n.Attr {
				k := strings.ToLower(a.Key)
				if strings.HasPrefix(k, "on") {
					t.Fatalf("output has event attribute %q: %q", a.Key, out)
				}
				if urlAttrs[k] {
					v := strings.Map(func(r rune) rune {
						if r <= ' ' || r == 0x7f {
							return -1
						}
						return r
					}, strings.ToLower(a.Val))
					if strings.Contains(v, "javascript:") || strings.Contains(v, "vbscript:") {
						t.Fatalf("output has script URL in %s: %q", a.Key, out)
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
}

// assertSafeCSS checks every CSS context of out (style attributes and
// <style> bodies, as a browser parses them): no @import, and every url()
// points only at this message's cid endpoint or a raster data: image.
func assertSafeCSS(t testing.TB, out string, msgID int64) {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(out))
	if err != nil {
		t.Fatalf("parse output: %v", err)
	}
	prefix := "/api/messages/" + strconv.FormatInt(msgID, 10) + "/cid/"
	check := func(css string) {
		l := asciiLower(css) // byte offsets must match css
		if strings.Contains(l, "@import") {
			t.Fatalf("output CSS has @import: %q", out)
		}
		for i := strings.Index(l, "url("); i >= 0; {
			arg, _ := cssURLArg(css[i+4:])
			if !strings.HasPrefix(arg, prefix) && !safeDataImage(arg) {
				t.Fatalf("output CSS url(%q) not local: %q", arg, out)
			}
			j := strings.Index(l[i+4:], "url(")
			if j < 0 {
				break
			}
			i += 4 + j
		}
	}
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			for _, a := range n.Attr {
				if a.Key == "style" {
					check(a.Val)
				}
			}
			if n.Data == "style" {
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					if c.Type == html.TextNode {
						check(c.Data)
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
}

func FuzzSanitizeHTML(f *testing.F) {
	seeds := []string{
		`<p>hi</p>`, `<script>alert(1)</script>`, `<img src=x onerror=alert(1)>`,
		`<a href="java&#x73;cript:alert(1)">x</a>`, `<svg><script>alert(1)</script></svg>`,
		`<style>@import url(http://x/);</style>`, `<div style="background:url(http://x/)">`,
		`<a href=" JaVaScRiPt:x">`, `<noscript><img src=x onerror=a()></noscript>`,
		`<math><mtext><table><mglyph><style><img src=x onerror=alert(1)>`,
		`<img src="cid:a%2F..%2F..">`, `<form action=x><input onfocus=a autofocus>`,
		"<a href=\"java\nscript:x\">", `<<script>script>alert(1)<</script>/script>`,
		`<style>p{}</style ><script>x</script>`, `<div style="x:ex\70 ression(a)">`,
		`<div style="background:u@import x;rl(http://evil/a.png)">`,
		`<style>@imp@import ;ort "http://evil/x.css"; body{}</style>`,
		`<div style="background:u@import;rl(//evil/p)">`, `<style>p{background:u@import;rl(//evil/p)}</style>`,
		`<div style="background:u&#x40;import;rl(http://evil/a.png)">`,
		`<style>@import url(cid:a); p{background:url("cid:b")}</style>`,
		`<body bgcolor="#fff" text=red style="background:url(http://x/)"><p>x</body>after`,
		`<body style="color:red"><body style="x:expression(a)">`,
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		out, remote := sanitizeHTML(in, 7)
		assertSafeHTML(t, out)
		assertSafeCSS(t, out, 7)
		if strings.Contains(out, "data-blocked-src") && !remote {
			t.Fatalf("blocked image but hasRemote=false: %q", out)
		}
		if (strings.HasPrefix(in, `<img src="http://`) || strings.HasPrefix(in, `<p style="background:url(http://`)) && !remote {
			t.Fatalf("remote reference removed but hasRemote=false: %q -> %q", in, out)
		}
		// Idempotence on the safety property: sanitising the output again
		// must also be safe (catches serialisation that re-parses differently).
		again, _ := sanitizeHTML(out, 7)
		assertSafeHTML(t, again)
		assertSafeCSS(t, again, 7)
	})
}

func TestSanitizeHTMLDeepNestingIsBounded(t *testing.T) {
	in := strings.Repeat("<b>", 200000) + strings.Repeat("</i>", 200000) + "end"
	start := time.Now()
	out, _ := sanitizeHTML(in, 1)
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("sanitising took %v", d)
	}
	if n := strings.Count(out, "<b>"); n > maxOpenElements {
		t.Fatalf("%d nested <b> emitted, want at most %d", n, maxOpenElements)
	}
	if !strings.HasSuffix(out, "end"+strings.Repeat("</b>", strings.Count(out, "<b>"))) {
		t.Fatalf("unexpected tail %q", out[max(0, len(out)-40):])
	}
}

func TestLocalCIDURLRejectsDotSegments(t *testing.T) {
	s := &sanitizer{msgID: 7}
	p := s.cidPrefix()
	for _, u := range []string{p + ".", p + "..", p + "%2e", p + "%2E", p + "%2e%2e", p + ".%2e", p + "%2E.", p + "%zz", p} {
		if s.localCIDURL(u) {
			t.Errorf("localCIDURL(%q) = true, want false", u)
		}
	}
	for _, u := range []string{p + "img1@x", p + "a.b", p + "...", p + "%2e%2e%2e", p + "part1.2%40host"} {
		if !s.localCIDURL(u) {
			t.Errorf("localCIDURL(%q) = false, want true", u)
		}
	}
}

func TestSanitizeCSSBansSrcFunction(t *testing.T) {
	for _, in := range []string{
		`<div style="background-image: src(http://e.vil/x.png)">t</div>`,
		`<div style="background-image: SRC( 'x.png' )">t</div>`,
		`<style>p { background: src("https://e.vil/") }</style><p>t</p>`,
	} {
		out, _ := sanitizeHTML(in, 1)
		if strings.Contains(strings.ToLower(out), "src(") {
			t.Errorf("sanitizeHTML(%q) = %q, still contains src(", in, out)
		}
	}
}

// The <body> element is dropped, but its colours and style
// are carried by a wrapping <div>, sanitised like any style.
func TestSanitizeHTMLBodyStyleKept(t *testing.T) {
	cases := []struct {
		name, in string
		want     []string
		reject   []string
		remote   bool
	}{
		{name: "bgcolor and text", in: `<html><body bgcolor="#FFEEDD" text="navy"><p>x</p></body></html>`,
			want: []string{`<div style="background-color: #FFEEDD; color: navy"><p>x</p></div>`}},
		{name: "style", in: `<body style="font-family: serif; margin: 0"><p>x</p></body>`,
			want: []string{`<div style="font-family: serif; margin: 0"><p>x</p></div>`}},
		{name: "bad colour dropped", in: `<body bgcolor="red;background:url(http://e.vil/)">x</body>`,
			want: []string{"x"}, reject: []string{"<div", "e.vil", "url("}},
		{name: "remote background style blocked", in: `<body style="background:url(http://e.vil/a.png)">x</body>`,
			want: []string{"x"}, reject: []string{"e.vil"}, remote: true},
		{name: "only the first body", in: `<body bgcolor="red"><body bgcolor="blue">x`,
			want: []string{`<div style="background-color: red">x</div>`}, reject: []string{"blue"}},
		{name: "plain body adds nothing", in: `<body onload="a()">x</body>`,
			want: []string{"x"}, reject: []string{"<div", "onload"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, remote := sanitizeHTML(c.in, 3)
			for _, w := range c.want {
				if !strings.Contains(out, w) {
					t.Errorf("output missing %q\nout: %s", w, out)
				}
			}
			for _, r := range c.reject {
				if strings.Contains(strings.ToLower(out), strings.ToLower(r)) {
					t.Errorf("output contains %q\nout: %s", r, out)
				}
			}
			if remote != c.remote {
				t.Errorf("hasRemote = %v, want %v", remote, c.remote)
			}
			assertSafeHTML(t, out)
			assertSafeCSS(t, out, 3)
		})
	}
}
