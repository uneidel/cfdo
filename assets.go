package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Cloudflare rejects a single static asset above this size.
const maxAssetSize = 25 << 20

// assetFile is one file under the assets directory, keyed by the URL path it
// is served at.
type assetFile struct {
	Path string // "/css/site.css"
	Disk string
	Hash string
	Size int64
}

type manifestEntry struct {
	Hash string `json:"hash"`
	Size int64  `json:"size"`
}

// scanAssets walks the assets directory. Hidden files and directories are
// skipped so editor droppings and .git never get published, and /__cfdo/ is
// refused because assets are matched before the worker runs and would
// shadow the admin routes.
func scanAssets(dir string) ([]assetFile, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("assets directory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("assets: %s is not a directory", dir)
	}
	var out []assetFile
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p != dir && strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		urlPath := "/" + filepath.ToSlash(rel)
		if urlPath == "/__cfdo" || strings.HasPrefix(urlPath, "/__cfdo/") {
			return fmt.Errorf("assets: %s would shadow cfdo's admin routes", urlPath)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if len(data) > maxAssetSize {
			return fmt.Errorf("assets: %s is %d bytes; Cloudflare's limit is %d per file", urlPath, len(data), maxAssetSize)
		}
		out = append(out, assetFile{Path: urlPath, Disk: p, Hash: assetHash(data, urlPath), Size: int64(len(data))})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// assetHash follows Cloudflare's reference implementation: the first 32 hex
// chars of sha256(base64(contents) + extension).
func assetHash(data []byte, name string) string {
	sum := sha256.Sum256([]byte(base64.StdEncoding.EncodeToString(data) + strings.TrimPrefix(path.Ext(name), ".")))
	return hex.EncodeToString(sum[:])[:32]
}

func assetsTotalSize(files []assetFile) int64 {
	var n int64
	for _, f := range files {
		n += f.Size
	}
	return n
}

// UploadAssets runs the direct-upload flow: register a manifest, upload only
// the buckets Cloudflare does not already have, and return the completion
// token that the script upload's metadata must carry.
func (c *Client) UploadAssets(ctx context.Context, accountID, script string, files []assetFile) (jwt string, uploaded int, err error) {
	manifest := make(map[string]manifestEntry, len(files))
	byHash := make(map[string]assetFile, len(files))
	for _, f := range files {
		manifest[f.Path] = manifestEntry{Hash: f.Hash, Size: f.Size}
		byHash[f.Hash] = f
	}
	body, err := json.Marshal(map[string]any{"manifest": manifest})
	if err != nil {
		return "", 0, err
	}

	var session struct {
		JWT     string     `json:"jwt"`
		Buckets [][]string `json:"buckets"`
	}
	_, err = c.request(ctx, http.MethodPost,
		fmt.Sprintf("/accounts/%s/workers/scripts/%s/assets-upload-session", url.PathEscape(accountID), url.PathEscape(script)),
		body, "application/json", &session)
	if err != nil {
		return "", 0, fmt.Errorf("starting asset upload session: %w", err)
	}
	if len(session.Buckets) == 0 {
		// Everything is already stored; the session token is the completion token.
		return session.JWT, 0, nil
	}

	// Bucket uploads authenticate with the session token, not the API token.
	uc := *c
	uc.Token = session.JWT
	completion := ""
	for _, bucket := range session.Buckets {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		for _, h := range bucket {
			f, ok := byHash[h]
			if !ok {
				return "", uploaded, fmt.Errorf("asset upload session asked for unknown hash %s", h)
			}
			data, err := os.ReadFile(f.Disk)
			if err != nil {
				return "", uploaded, err
			}
			ct := mime.TypeByExtension(path.Ext(f.Path))
			if ct == "" {
				ct = "application/octet-stream"
			}
			mh := make(textproto.MIMEHeader)
			mh.Set("Content-Disposition", fmt.Sprintf("form-data; name=%q; filename=%q", h, h))
			mh.Set("Content-Type", ct)
			part, err := mw.CreatePart(mh)
			if err != nil {
				return "", uploaded, err
			}
			if _, err := part.Write([]byte(base64.StdEncoding.EncodeToString(data))); err != nil {
				return "", uploaded, err
			}
		}
		if err := mw.Close(); err != nil {
			return "", uploaded, err
		}
		var res struct {
			JWT string `json:"jwt"`
		}
		_, err := uc.request(ctx, http.MethodPost,
			fmt.Sprintf("/accounts/%s/workers/assets/upload?base64=true", url.PathEscape(accountID)),
			buf.Bytes(), mw.FormDataContentType(), &res)
		if err != nil {
			return "", uploaded, fmt.Errorf("uploading assets: %w", err)
		}
		uploaded += len(bucket)
		if res.JWT != "" {
			completion = res.JWT
		}
	}
	if completion == "" {
		return "", uploaded, fmt.Errorf("asset upload finished without a completion token")
	}
	return completion, uploaded, nil
}
