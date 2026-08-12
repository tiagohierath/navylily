package main

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestCacheHeaderHelpers(t *testing.T) {
	w := httptest.NewRecorder()
	setEdgeCache(w, 300)
	if got, want := w.Header().Get("Cache-Control"), "public, max-age=0, must-revalidate, s-maxage=300"; got != want {
		t.Fatalf("edge cache header = %q, want %q", got, want)
	}

	w = httptest.NewRecorder()
	setImmutableCache(w)
	if got, want := w.Header().Get("Cache-Control"), "public, max-age=31536000, immutable"; got != want {
		t.Fatalf("immutable cache header = %q, want %q", got, want)
	}
}

func TestStaticCachePolicy(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"root.html":  "home",
		"wiki.html":  "wiki",
		"sw.js":      "worker",
		"search.txt": "index",
		"photo.webp": "image",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "wiki"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "wiki", "drawing.html"), []byte("article"), 0o644); err != nil {
		t.Fatal(err)
	}

	oldPublicDir := cfg.PublicDir
	cfg.PublicDir = dir
	t.Cleanup(func() { cfg.PublicDir = oldPublicDir })

	tests := []struct {
		url  string
		want string
	}{
		{"/", "public, max-age=0, must-revalidate, s-maxage=300"},
		{"/wiki", "public, max-age=0, must-revalidate, s-maxage=300"},
		{"/wiki/drawing.html", "public, max-age=0, must-revalidate, s-maxage=300"},
		{"/search.txt", "public, max-age=0, must-revalidate, s-maxage=300"},
		{"/photo.webp", "public, max-age=0, must-revalidate, s-maxage=86400"},
		{"/photo.webp?v=2", "public, max-age=31536000, immutable"},
		{"/sw.js", "no-store, no-cache, must-revalidate"},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			r := httptest.NewRequest("GET", tt.url, nil)
			w := httptest.NewRecorder()
			handleStatic(w, r)
			if w.Code != 200 {
				t.Fatalf("status = %d, want 200", w.Code)
			}
			if got := w.Header().Get("Cache-Control"); got != tt.want {
				t.Fatalf("Cache-Control = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLessonSlugOnlyMatchesNumberedLessons(t *testing.T) {
	tests := map[string]string{
		"/001.html":                    "001",
		"/protected/042.html":          "protected/042",
		"/wiki/perspective.html":       "",
		"/art-sovereignty.html":        "",
		"/protected/index.html":        "",
		"/protected/not-a-lesson.html": "",
	}
	for path, want := range tests {
		if got := lessonSlug(path); got != want {
			t.Errorf("lessonSlug(%q) = %q, want %q", path, got, want)
		}
	}
}
