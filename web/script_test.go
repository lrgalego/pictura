package web

import (
	"strings"
	"testing"
)

func TestScriptFromHTML(t *testing.T) {
	posted := `<p>INT. LIGHTHOUSE. NIGHT.</p><p>MARA: Hello?<br>PIP: Please don&#39;t scream.</p><div>A car <b>pulls up</b>.</div><script>alert(1)</script><p onclick="x()">Fin.</p>`
	clean, text := scriptFromHTML(posted)
	if strings.Contains(clean, "<script") || strings.Contains(clean, "onclick") {
		t.Fatalf("unsafe HTML survived: %s", clean)
	}
	want := "INT. LIGHTHOUSE. NIGHT.\n\nMARA: Hello?\nPIP: Please don't scream.\n\nA car pulls up.\n\nFin."
	if text != want {
		t.Fatalf("text:\n%q\nwant\n%q", text, want)
	}
	// Runs of empty paragraphs collapse to one blank line; nbsp becomes space.
	_, text = scriptFromHTML(`<p>a</p><p></p><p><br></p><p>b&nbsp;c</p>`)
	if text != "a\n\nb c" {
		t.Fatalf("collapsed: %q", text)
	}
}

func TestHTMLFromTextRoundTrip(t *testing.T) {
	text := "INT. LIGHTHOUSE. NIGHT.\r\n\r\nMARA: <Hello> & goodbye\nPIP: Sure."
	h := htmlFromText(text)
	if h != "<p>INT. LIGHTHOUSE. NIGHT.</p><p>MARA: &lt;Hello&gt; &amp; goodbye<br>PIP: Sure.</p>" {
		t.Fatalf("html: %s", h)
	}
	_, back := scriptFromHTML(h)
	if back != "INT. LIGHTHOUSE. NIGHT.\n\nMARA: <Hello> & goodbye\nPIP: Sure." {
		t.Fatalf("round trip: %q", back)
	}
	if htmlFromText("  \n\n ") != "" {
		t.Fatal("blank text renders nothing")
	}
}

func TestScriptInputPrefersTheEditor(t *testing.T) {
	clean, text := scriptInput("<p>from editor</p>", "from textarea")
	if clean != "<p>from editor</p>" || text != "from editor" {
		t.Fatalf("editor input: %q %q", clean, text)
	}
	clean, text = scriptInput("  ", "plain\r\nlines\r\n\r\n\r\n\r\nmore")
	if text != "plain\nlines\n\nmore" || clean != "<p>plain<br>lines</p><p>more</p>" {
		t.Fatalf("textarea input: %q %q", clean, text)
	}
}
