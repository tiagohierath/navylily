package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLoginPageRendering(t *testing.T) {
	cases := []struct {
		url      string
		contains []string
		excludes []string
	}{
		{"/login", nil, []string{"<!--ERRO-->", "role=\"alert\""}},
		{"/login?erro=1", []string{"E-mail ou senha incorretos", "role=\"alert\""}, []string{"<!--ERRO-->"}},
		{"/login?erro=confirma&email=a%40b.com", []string{
			"ainda não foi confirmada", "/auth/resend", `value="a@b.com"`,
		}, nil},
		{`/login?erro=confirma&email=%22%3E%3Cscript%3E`, nil, []string{"\"><script>"}},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", c.url, nil)
		w := httptest.NewRecorder()
		handleLoginPage(w, r)
		body := w.Body.String()
		if w.Code != 200 {
			t.Fatalf("%s: status %d", c.url, w.Code)
		}
		for _, s := range c.contains {
			if !strings.Contains(body, s) {
				t.Errorf("%s: missing %q", c.url, s)
			}
		}
		for _, s := range c.excludes {
			if strings.Contains(body, s) {
				t.Errorf("%s: should not contain %q", c.url, s)
			}
		}
	}
}

func TestSignupPageRendering(t *testing.T) {
	r := httptest.NewRequest("GET", "/signup?erro=senha", nil)
	w := httptest.NewRecorder()
	handleSignupPage(w, r)
	if !strings.Contains(w.Body.String(), "entre 8 e 72 caracteres") {
		t.Errorf("signup senha error not rendered")
	}
	r = httptest.NewRequest("GET", "/signup", nil)
	w = httptest.NewRecorder()
	handleSignupPage(w, r)
	if strings.Contains(w.Body.String(), "<!--ERRO-->") {
		t.Errorf("marker left in clean signup page")
	}
}
