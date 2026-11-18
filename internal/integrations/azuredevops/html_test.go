package azuredevops

import (
	"strings"
	"testing"
)

func TestPlainDescriptionKeepsParagraphsAndDropsActiveContent(t *testing.T) {
	got, e := PlainDescription(`<div>第一段 &amp; 内容</div><p>第二段<br>next <b>bold</b></p><script>alert('secret')</script><style>body{secret:1}</style><p><a href="javascript:alert(1)">链接</a><img src="https://invalid.example/do-not-fetch" onerror="secret()"></p><pre>  one
    two</pre>`)
	want := "第一段 & 内容\n第二段\nnext bold\n链接\n  one\n    two"
	if e != nil || got != want {
		t.Fatalf("text=%q error=%v", got, e)
	}
}
func TestPlainDescriptionMalformedNamespacesLimitsAndEntities(t *testing.T) {
	for _, source := range []string{`<script/>hidden</script><p>shown</p>`, `<svg><foreignObject><script>hidden</script></foreignObject></svg><p>shown</p>`, `<template><p>hidden</p></template><p>shown</p>`} {
		got, e := PlainDescription(source)
		if e != nil || strings.Contains(got, "hidden") || !strings.Contains(got, "shown") {
			t.Fatalf("unsafe conversion %q %v", got, e)
		}
	}
	got, e := PlainDescription(`<p>&lt;script&gt;literal&lt;/script&gt;</p>`)
	if e != nil || got != "<script>literal</script>" {
		t.Fatalf("plain escaped text %q %v", got, e)
	} // consumer must use PlainNotes.
	if _, e = PlainDescription(strings.Repeat("x", MaxCellBytes+1)); e == nil {
		t.Fatal("oversize silently truncated")
	}
	if _, e = PlainDescription(string([]byte{0xff})); e == nil {
		t.Fatal("invalid UTF8 accepted")
	}
}

func TestPlainDescriptionWhitespaceAndDefinitionLists(t *testing.T) {
	for _, tc := range []struct{ html, want string }{
		{`a<span>&nbsp;b</span>`, "a b"},
		{`<span>a&nbsp;</span>b`, "a b"},
		{"<pre>  first\n    second</pre>", "  first\n    second"},
		{"<p>before</p><pre>line  \nnext</pre>", "before\nline  \nnext"},
		{"<dl><dt>Term</dt><dd>Definition</dd></dl>", "Term\nDefinition"},
	} {
		got, e := PlainDescription(tc.html)
		if e != nil || got != tc.want {
			t.Errorf("%q => %q want %q; %v", tc.html, got, tc.want, e)
		}
	}
}
