package mail

import "testing"

func TestTextFromHTML(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"empty", "", ""},
		{"paragraphs and entities", "<p>Hello <b>world</b></p><p>Second&nbsp;para &amp; more</p>", "Hello world\nSecond para & more"},
		{"head script style dropped", "<html><head><title>T</title><style>p{color:red}</style><script>alert(1)</script></head><body>Line1<br>Line2<div>Block</div></body></html>", "Line1\nLine2\nBlock"},
		{"inline", "<b>a</b> <i>b</i>", "a b"},
		{"blocks", "<div>one</div><div>two</div>", "one\ntwo"},
		{"br and p", "a<br>b<br/>c<p>d</p>e", "a\nb\nc\nd\ne"},
		{"script style inline", "x<script>alert('hi')</script><style>p{color:red}</style>y", "xy"},
		{"script uppercase with tag inside", "x<SCRIPT type=text/javascript>var a='<b>';</SCRIPT>y", "xy"},
		{"entities", "a &amp; b &lt;c&gt; &quot;d&quot; &nbsp;e &eacute; &#233; &#x41;", "a & b <c> \"d\" e é é A"},
		{"whitespace collapse", "  a \t\n  b   \n\n\n  c  ", "a b c"},
		{"plain text", "plain   text\n\n\n\nmore", "plain text more"},
		{"comment dropped", "a<!-- hidden <p>x</p> -->b", "ab"},
		{"list", "<ul><li>a</li><li>b</li></ul>", "a\nb"},
		{"attr with >", `<a title="x>y">link</a>`, "link"},
		{"literal less-than", "1 < 2 and 3 > 2", "1 < 2 and 3 > 2"},
		{"unterminated tag", "text <b unclosed", "text"},
		{"unclosed tags", "<p>unclosed <b>tags", "unclosed tags"},
		{"head title dropped", "<head><title>T</title></head><body>B</body>", "B"},
		{"table", "<table><tr><td>a</td><td>b</td></tr><tr><td>c</td></tr></table>", "a b\nc"},
		{"html5 blocks", "<nav>n</nav><main>m</main><aside>a</aside><fieldset>f</fieldset><table><caption>c</caption></table>", "n\nm\na\nf\nc"},
		{"noscript and template dropped", "a<noscript>x</noscript><template>y</template>b", "ab"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := TextFromHTML(tt.in); got != tt.want {
				t.Errorf("TextFromHTML(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
