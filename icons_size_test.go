package main

import (
	"os"
	"regexp"
	"testing"
)

// TestIconsHaveASize: #293. An <svg> with no width or height takes the
// browser's 300×150 default wherever no CSS sizes it. iconViewedCircle was
// sized only in the file tree, so reused in the file header it filled the
// row. Every icon carries its own size; CSS may still override it.
func TestIconsHaveASize(t *testing.T) {
	src, err := os.ReadFile("templates/icons.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	define := regexp.MustCompile(`(?s)\{\{-? define "(\w+)" -?\}\}\s*(<svg[^>]*>)`)
	sized := regexp.MustCompile(`\swidth="[^"]+"[^>]*\sheight="[^"]+"|\sheight="[^"]+"[^>]*\swidth="[^"]+"`)
	n := 0
	for _, m := range define.FindAllSubmatch(src, -1) {
		n++
		if !sized.Match(m[2]) {
			t.Errorf("%s: <svg> has no width and height: %s", m[1], m[2])
		}
	}
	if n < 10 {
		t.Fatalf("found only %d icons; the pattern no longer matches icons.tmpl", n)
	}
}
