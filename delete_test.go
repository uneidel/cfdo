package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// deleteAPI wraps listAPI and records script deletions instead of serving them.
func deleteAPI(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	inner := listAPI(t)
	t.Cleanup(inner.Close)
	var mu sync.Mutex
	var deleted []string
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			mu.Lock()
			deleted = append(deleted, r.URL.Path+"?"+r.URL.RawQuery)
			mu.Unlock()
			json.NewEncoder(rw).Encode(map[string]any{"success": true, "errors": []any{}, "result": nil})
			return
		}
		r.URL.Host, r.URL.Scheme, r.RequestURI = strings.TrimPrefix(inner.URL, "http://"), "http", ""
		resp, err := http.DefaultTransport.RoundTrip(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		rw.WriteHeader(resp.StatusCode)
		var body any
		json.NewDecoder(resp.Body).Decode(&body)
		json.NewEncoder(rw).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), deleted...)
	}
}

func accountEnv(t *testing.T, apiURL string) {
	t.Setenv("CFDO_HOME", t.TempDir())
	t.Setenv("CFDO_API_BASE", apiURL)
	t.Setenv("CLOUDFLARE_API_TOKEN", "tok")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct")
	t.Chdir(t.TempDir()) // no cfdo.json up the tree
}

func TestDeleteByNamespaceDeletesOwningScript(t *testing.T) {
	api, deleted := deleteAPI(t)
	accountEnv(t, api.URL)

	out := captureStdout(t, func() {
		if err := execute(context.Background(), "delete", "--ns", "ns-c", "-y"); err != nil {
			t.Fatalf("delete: %v", err)
		}
	})
	got := deleted()
	if len(got) != 1 || got[0] != "/accounts/acct/workers/scripts/alpha-app?force=true" {
		t.Fatalf("want one forced delete of alpha-app, got %v", got)
	}
	// both of alpha-app's namespaces go, not just the one named
	if !strings.Contains(out, "ns-a") || !strings.Contains(out, "ns-c") {
		t.Fatalf("warning should list every namespace being deleted:\n%s", out)
	}
}

func TestDeleteRefusesWithoutConfirmation(t *testing.T) {
	api, deleted := deleteAPI(t)
	accountEnv(t, api.URL)

	var err error
	captureStdout(t, func() { err = execute(context.Background(), "delete", "alpha-app") })
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("want a refusal mentioning --yes, got %v", err)
	}
	if got := deleted(); len(got) != 0 {
		t.Fatalf("nothing should be deleted, got %v", got)
	}
}

func TestDeleteRejectsBadTargets(t *testing.T) {
	api, deleted := deleteAPI(t)
	accountEnv(t, api.URL)

	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"delete", "-y"}, "name a script"},
		{[]string{"delete", "alpha-app", "--ns", "ns-a", "-y"}, "not both"},
		{[]string{"delete", "--ns", "nope", "-y"}, "no Durable Object namespace"},
		{[]string{"delete", "zeta-app", "-y"}, "not deployed"},
		{[]string{"delete", "ghost", "-y"}, "no script"},
	} {
		err := execute(context.Background(), tc.args...)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: want error containing %q, got %v", tc.args, tc.want, err)
		}
	}
	if got := deleted(); len(got) != 0 {
		t.Fatalf("nothing should be deleted, got %v", got)
	}
}

func TestStatusByScriptOrNamespace(t *testing.T) {
	api, _ := deleteAPI(t)
	accountEnv(t, api.URL)

	for _, args := range [][]string{
		{"status", "alpha-app", "--json", "--no-ping"},
		{"status", "--ns", "ns-a", "--json", "--no-ping"},
	} {
		out := captureStdout(t, func() {
			if err := execute(context.Background(), args...); err != nil {
				t.Fatalf("%v: %v", args, err)
			}
		})
		var got struct {
			Script     string            `json:"script"`
			Deployed   bool              `json:"deployed"`
			Namespaces []remoteNamespace `json:"namespaces"`
		}
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("%v: invalid JSON: %v\n%s", args, err, out)
		}
		if got.Script != "alpha-app" || !got.Deployed || len(got.Namespaces) != 2 {
			t.Fatalf("%v: wrong report: %+v", args, got)
		}
	}

	// an undeployed script with a namespace still reports, marked as such
	out := captureStdout(t, func() {
		if err := execute(context.Background(), "status", "--ns", "ns-b", "--json"); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, `"deployed": false`) || !strings.Contains(out, `"zeta-app"`) {
		t.Fatalf("want zeta-app reported as undeployed:\n%s", out)
	}

	if err := execute(context.Background(), "status", "alpha-app", "-c", "x.json"); err == nil {
		t.Fatal("status with both a script and -c should fail")
	}
}
