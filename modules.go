package main

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// relImportRe matches the relative specifiers of static imports, re-exports
// and dynamic import() calls, including minified forms like `from"./x.wasm"`.
// Bare specifiers (cloudflare:workers, npm names) are left to Cloudflare.
var relImportRe = regexp.MustCompile(`(?:\bfrom|\bimport)\s*\(?\s*["'](\.{1,2}/[^"'\s]+)["']`)

// moduleTypes maps file extensions to the content types Cloudflare's script
// upload API uses to pick a module type.
var moduleTypes = map[string]string{
	".js":   "application/javascript+module",
	".mjs":  "application/javascript+module",
	".wasm": "application/wasm",
	".txt":  "text/plain",
	".html": "text/plain",
	".bin":  "application/octet-stream",
}

// collectModules returns the main module and every module reachable from it
// through relative imports, named by their path relative to dir.
func collectModules(dir, main string) ([]Module, error) {
	var out []Module
	seen := map[string]bool{}
	var visit func(name, from string) error
	visit = func(name, from string) error {
		if seen[name] {
			return nil
		}
		seen[name] = true
		if name == ".." || strings.HasPrefix(name, "../") {
			return fmt.Errorf("%s imports %s, which is outside the project directory", from, name)
		}
		ct, ok := moduleTypes[strings.ToLower(path.Ext(name))]
		if !ok {
			return fmt.Errorf("%s imports %s: unsupported module type (want .js, .mjs, .wasm, .txt, .html or .bin)", from, name)
		}
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
		if os.IsNotExist(err) && from != "" {
			return fmt.Errorf("%s imports %s, which does not exist", from, name)
		}
		if err != nil {
			return fmt.Errorf("reading module %s: %w", name, err)
		}
		out = append(out, Module{Name: name, ContentType: ct, Data: data})
		if ct != "application/javascript+module" {
			return nil
		}
		for _, m := range relImportRe.FindAllStringSubmatch(string(data), -1) {
			dep := path.Clean(path.Join(path.Dir(name), m[1]))
			if err := visit(dep, name); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(path.Clean(filepath.ToSlash(main)), ""); err != nil {
		return nil, err
	}
	return out, nil
}

func modulesSize(mods []Module) int {
	n := 0
	for _, m := range mods {
		n += len(m.Data)
	}
	return n
}
