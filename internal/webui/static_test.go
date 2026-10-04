package webui

import (
	"compress/gzip"
	"io"
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

func TestBundleHandlerCachesHashedAssetsAndRevalidatesTheShell(t *testing.T) {
	bundle := fstest.MapFS{
		"index.html":          {Data: []byte(`<div id="root"></div>`)},
		"assets/index-abc.js": {Data: []byte(strings.Repeat("console.log(1);", 100))},
		"favicon.ico":         {Data: []byte("icon")},
	}
	serve := func(target string, headers map[string]string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		for key, value := range headers {
			request.Header.Set(key, value)
		}
		recorder := httptest.NewRecorder()
		handler(bundle).ServeHTTP(recorder, request)
		return recorder
	}

	asset := serve("/assets/index-abc.js", map[string]string{"Accept-Encoding": "gzip"})
	if asset.Code != http.StatusOK || asset.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" ||
		asset.Header().Get("Content-Encoding") != "gzip" || asset.Header().Get("Vary") != "Accept-Encoding" ||
		!strings.HasPrefix(asset.Header().Get("Content-Type"), "text/javascript") {
		t.Fatalf("asset status=%d headers=%v", asset.Code, asset.Header())
	}
	reader, err := gzip.NewReader(asset.Body)
	if err != nil {
		t.Fatal(err)
	}
	if body, _ := io.ReadAll(reader); string(body) != strings.Repeat("console.log(1);", 100) {
		t.Fatalf("gzipped body = %q", body)
	}
	plain := serve("/assets/index-abc.js", map[string]string{"Accept-Encoding": "gzip;q=0"})
	if plain.Header().Get("Content-Encoding") != "" || plain.Header().Get("ETag") == asset.Header().Get("ETag") ||
		plain.Body.String() != strings.Repeat("console.log(1);", 100) {
		t.Fatalf("identity headers=%v", plain.Header())
	}
	if revalidated := serve("/assets/index-abc.js", map[string]string{
		"Accept-Encoding": "gzip", "If-None-Match": asset.Header().Get("ETag"),
	}); revalidated.Code != http.StatusNotModified {
		t.Fatalf("gzip revalidation status=%d", revalidated.Code)
	}

	for _, target := range []string{"/", "/scans/schedules/sch-1"} {
		shell := serve(target, nil)
		etag := shell.Header().Get("ETag")
		if shell.Code != http.StatusOK || shell.Header().Get("Cache-Control") != "no-cache" || etag == "" {
			t.Fatalf("%s status=%d headers=%v", target, shell.Code, shell.Header())
		}
		if revalidated := serve(target, map[string]string{"If-None-Match": etag}); revalidated.Code != http.StatusNotModified {
			t.Fatalf("%s revalidation status=%d", target, revalidated.Code)
		}
	}

	icon := serve("/favicon.ico", nil)
	etag := icon.Header().Get("ETag")
	if icon.Code != http.StatusOK || etag == "" || icon.Header().Get("Cache-Control") != "public, max-age=86400" {
		t.Fatalf("icon status=%d headers=%v", icon.Code, icon.Header())
	}
	if revalidated := serve("/favicon.ico", map[string]string{"If-None-Match": etag}); revalidated.Code != http.StatusNotModified {
		t.Fatalf("icon revalidation status=%d", revalidated.Code)
	}
}
