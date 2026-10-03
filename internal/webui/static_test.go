package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestHandlerServesIndexWithoutRedirect(t *testing.T) {
	handler := Handler()
	for _, target := range []string{"/", "/index.html", "/cleanup/cln-example"} {
		t.Run(target, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
			}
			if location := recorder.Header().Get("Location"); location != "" {
				t.Fatalf("unexpected redirect location %q", location)
			}
			if body := recorder.Body.String(); !strings.Contains(body, `<div id="root">`) {
				t.Fatalf("response did not look like index.html: %q", body)
			}
		})
	}
}

func TestHandlerReturnsNotFoundForMissingAssets(t *testing.T) {
	recorder := httptest.NewRecorder()
	Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/assets/missing.js", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNotFound)
	}
}

func TestBundleHandlerServesConsoleRoutesThatShareTheAssetsFolderName(t *testing.T) {
	bundle := fstest.MapFS{
		"index.html":          {Data: []byte(`<div id="root"></div>`)},
		"assets/index-abc.js": {Data: []byte("console.log(1)")},
	}
	for target, want := range map[string]struct {
		status int
		body   string
	}{
		"/assets":                      {http.StatusOK, `<div id="root">`},
		"/assets/":                     {http.StatusOK, `<div id="root">`},
		"/assets/ast-23456789abcdefgh": {http.StatusOK, `<div id="root">`},
		"/assets/index-abc.js":         {http.StatusOK, "console.log(1)"},
		"/assets/index-old.js":         {http.StatusNotFound, ""},
		"/scans/schedules/sch-1":       {http.StatusOK, `<div id="root">`},
	} {
		t.Run(target, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler(bundle).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
			if recorder.Code != want.status || !strings.Contains(recorder.Body.String(), want.body) {
				t.Fatalf("status=%d body=%q", recorder.Code, recorder.Body.String())
			}
			if strings.Contains(recorder.Body.String(), "index-abc.js</a>") {
				t.Fatal("served a directory listing")
			}
		})
	}
	recorder := httptest.NewRecorder()
	Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/assets/ast-23456789abcdefgh", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("placeholder asset detail status = %d", recorder.Code)
	}
}
