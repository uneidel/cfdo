package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCollectModulesFollowsRelativeImports(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"worker.mjs": `import { DurableObject } from "cloudflare:workers";
// the export/import has to run inside the object
import { a } from "./lib/a.js";
export { b } from './lib/a.js';
const lazy = () => import("./lazy.mjs");`,
		"lib/a.js":         `import w from"../plugins/p/x.wasm";export const a=1,b=2;`,
		"lazy.mjs":         `export default 1;`,
		"plugins/p/x.wasm": "\x00asm",
		"unused.js":        `export const u = 1;`,
	})
	mods, err := collectModules(dir, "worker.mjs")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range mods {
		got = append(got, m.Name+" "+m.ContentType)
	}
	want := []string{
		"worker.mjs application/javascript+module",
		"lib/a.js application/javascript+module",
		"plugins/p/x.wasm application/wasm",
		"lazy.mjs application/javascript+module",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("modules:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestCollectModulesRejectsBadImports(t *testing.T) {
	for name, src := range map[string]string{
		"does not exist":      `import x from "./missing.js";`,
		"outside the project": `import x from "../elsewhere.js";`,
		"unsupported":         `import x from "./data.json";`,
	} {
		dir := t.TempDir()
		writeFiles(t, dir, map[string]string{"worker.mjs": src})
		if _, err := collectModules(dir, "worker.mjs"); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func testPlugin(t *testing.T, version string) string {
	t.Helper()
	src := t.TempDir()
	writeFiles(t, src, map[string]string{
		"cfdo-plugin.json": `{"name":"demo-lib","version":"` + version + `","worker":"dist/w","entry":"lib.js","skill":"SKILL.md"}`,
		"dist/w/lib.js":    `import m from "./lib.wasm"; export const v = "` + version + `";`,
		"dist/w/lib.wasm":  "\x00asm",
		"SKILL.md":         "---\nname: demo-lib\ndescription: test\n---\n\n# demo-lib\n",
		"src/ignored.rs":   "fn main() {}",
	})
	return src
}

func TestCreateWithPluginAndUpdate(t *testing.T) {
	t.Setenv("CFDO_HOME", t.TempDir())
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct")
	t.Chdir(t.TempDir())
	src := testPlugin(t, "1")

	captureStdout(t, func() {
		if err := execute(context.Background(), "create", "app", "--plugin", src); err != nil {
			t.Fatal(err)
		}
	})
	for _, f := range []string{"plugins/demo-lib/lib.js", "plugins/demo-lib/lib.wasm"} {
		if _, err := os.Stat(filepath.Join("app", f)); err != nil {
			t.Error(err)
		}
	}
	if _, err := os.Stat("app/plugins/demo-lib/ignored.rs"); err == nil {
		t.Error("vendored files outside the worker directory")
	}
	skill, err := os.ReadFile("app/.claude/skills/demo-lib/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(skill), "---\nname: demo-lib\n") || !strings.Contains(string(skill), `"./plugins/demo-lib/lib.js"`) {
		t.Errorf("skill frontmatter or import note wrong:\n%s", skill)
	}

	lockOf := func() PluginLock {
		var c Config
		b, _ := os.ReadFile("app/cfdo.json")
		if err := json.Unmarshal(b, &c); err != nil || len(c.Plugins) != 1 {
			t.Fatalf("plugins in cfdo.json: %v %+v", err, c.Plugins)
		}
		return c.Plugins[0]
	}
	before := lockOf()
	if before.Name != "demo-lib" || before.Version != "1" || before.Source != src {
		t.Fatalf("lock = %+v", before)
	}

	// The plugin changes upstream; update pulls it in and records the new hash.
	writeFiles(t, src, map[string]string{
		"cfdo-plugin.json": `{"name":"demo-lib","version":"2","worker":"dist/w","entry":"lib.js","skill":"SKILL.md"}`,
		"dist/w/lib.js":    `export const v = "2";`,
	})
	os.Remove(filepath.Join(src, "dist/w/lib.wasm"))
	out := captureStdout(t, func() {
		if err := execute(context.Background(), "plugin", "update", "-c", "app/cfdo.json"); err != nil {
			t.Fatal(err)
		}
	})
	after := lockOf()
	if after.Version != "2" || after.SHA256 == before.SHA256 || !strings.Contains(out, "Updated") {
		t.Fatalf("after update: %+v\n%s", after, out)
	}
	if _, err := os.Stat("app/plugins/demo-lib/lib.wasm"); err == nil {
		t.Error("file removed upstream is still vendored")
	}
}

func TestPluginManifestRejectsEscapingPaths(t *testing.T) {
	src := t.TempDir()
	writeFiles(t, src, map[string]string{
		"cfdo-plugin.json": `{"name":"x","worker":"../../etc","entry":"passwd"}`,
	})
	if _, err := readPluginManifest(src); err == nil {
		t.Fatal("accepted a worker dir outside the plugin")
	}
}
