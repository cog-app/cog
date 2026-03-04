package volume

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"time"
)

type webdavBackend struct {
	cfg    *WebDAVConfig
	client *http.Client
}

func newWebDAVBackend(cfg *WebDAVConfig) *webdavBackend {
	return &webdavBackend{
		cfg:    cfg,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

func (b *webdavBackend) url(p string) string {
	base := strings.TrimRight(b.cfg.URL, "/")
	cleaned := path.Clean("/" + p)
	return base + cleaned
}

func (b *webdavBackend) auth(req *http.Request) {
	switch b.cfg.AuthType {
	case "bearer":
		req.Header.Set("Authorization", "Bearer "+b.cfg.Token)
	case "basic":
		req.SetBasicAuth(b.cfg.Username, b.cfg.Password)
	default:
		if b.cfg.Username != "" {
			req.SetBasicAuth(b.cfg.Username, b.cfg.Password)
		}
	}
}

func (b *webdavBackend) do(req *http.Request) (*http.Response, error) {
	b.auth(req)
	return b.client.Do(req)
}

// WebDAV XML types for PROPFIND responses.
type davMultistatus struct {
	XMLName   xml.Name      `xml:"DAV: multistatus"`
	Responses []davResponse `xml:"response"`
}

type davResponse struct {
	Href     string       `xml:"href"`
	Propstat []davPropStat `xml:"propstat"`
}

type davPropStat struct {
	Prop   davProp `xml:"prop"`
	Status string  `xml:"status"`
}

type davProp struct {
	DisplayName  string `xml:"displayname"`
	ContentLen   int64  `xml:"getcontentlength"`
	LastModified string `xml:"getlastmodified"`
	ContentType  string `xml:"getcontenttype"`
	ResourceType struct {
		Collection *struct{} `xml:"collection"`
	} `xml:"resourcetype"`
}

func (b *webdavBackend) List(dirPath string) ([]FileInfo, error) {
	u := b.url(dirPath)
	if !strings.HasSuffix(u, "/") {
		u += "/"
	}

	body := `<?xml version="1.0" encoding="utf-8"?>
<d:propfind xmlns:d="DAV:">
  <d:prop>
    <d:displayname/>
    <d:getcontentlength/>
    <d:getlastmodified/>
    <d:getcontenttype/>
    <d:resourcetype/>
  </d:prop>
</d:propfind>`

	req, err := http.NewRequest("PROPFIND", u, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Depth", "1")
	req.Header.Set("Content-Type", "application/xml")

	resp, err := b.do(req)
	if err != nil {
		return nil, fmt.Errorf("propfind: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 207 {
		return nil, fmt.Errorf("propfind: status %d", resp.StatusCode)
	}

	var ms davMultistatus
	if err := xml.NewDecoder(resp.Body).Decode(&ms); err != nil {
		return nil, fmt.Errorf("parse propfind response: %w", err)
	}

	// The first response is the directory itself; skip it.
	var items []FileInfo
	for i, r := range ms.Responses {
		if i == 0 {
			continue
		}
		fi := b.responseToFileInfo(r, dirPath)
		items = append(items, fi)
	}
	return items, nil
}

func (b *webdavBackend) responseToFileInfo(r davResponse, parentPath string) FileInfo {
	var prop davProp
	for _, ps := range r.Propstat {
		if strings.Contains(ps.Status, "200") {
			prop = ps.Prop
			break
		}
	}

	name := prop.DisplayName
	if name == "" {
		name = path.Base(r.Href)
	}

	isDir := prop.ResourceType.Collection != nil
	fi := FileInfo{
		Name:     name,
		Path:     path.Join(parentPath, name),
		Size:     prop.ContentLen,
		IsDir:    isDir,
		MimeType: prop.ContentType,
	}

	if prop.LastModified != "" {
		if t, err := http.ParseTime(prop.LastModified); err == nil {
			fi.ModTime = t
		}
	}
	return fi
}

func (b *webdavBackend) Stat(filePath string) (*FileInfo, error) {
	req, err := http.NewRequest("PROPFIND", b.url(filePath), strings.NewReader(
		`<?xml version="1.0" encoding="utf-8"?>
<d:propfind xmlns:d="DAV:">
  <d:prop>
    <d:displayname/>
    <d:getcontentlength/>
    <d:getlastmodified/>
    <d:getcontenttype/>
    <d:resourcetype/>
  </d:prop>
</d:propfind>`))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Depth", "0")
	req.Header.Set("Content-Type", "application/xml")

	resp, err := b.do(req)
	if err != nil {
		return nil, fmt.Errorf("propfind: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return nil, fmt.Errorf("not found: %s", filePath)
	}
	if resp.StatusCode != 207 {
		return nil, fmt.Errorf("propfind: status %d", resp.StatusCode)
	}

	var ms davMultistatus
	if err := xml.NewDecoder(resp.Body).Decode(&ms); err != nil {
		return nil, fmt.Errorf("parse propfind: %w", err)
	}
	if len(ms.Responses) == 0 {
		return nil, fmt.Errorf("no response for %s", filePath)
	}

	fi := b.responseToFileInfo(ms.Responses[0], path.Dir(filePath))
	fi.Path = filePath
	return &fi, nil
}

func (b *webdavBackend) Get(filePath string) (io.ReadCloser, error) {
	req, err := http.NewRequest("GET", b.url(filePath), nil)
	if err != nil {
		return nil, err
	}
	resp, err := b.do(req)
	if err != nil {
		return nil, fmt.Errorf("get: %w", err)
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		return nil, fmt.Errorf("get: status %d", resp.StatusCode)
	}
	return resp.Body, nil
}

func (b *webdavBackend) Put(filePath string, content io.Reader) error {
	req, err := http.NewRequest("PUT", b.url(filePath), content)
	if err != nil {
		return err
	}
	resp, err := b.do(req)
	if err != nil {
		return fmt.Errorf("put: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 201 && resp.StatusCode != 204 && resp.StatusCode != 200 {
		return fmt.Errorf("put: status %d", resp.StatusCode)
	}
	return nil
}

func (b *webdavBackend) Mkdir(dirPath string) error {
	u := b.url(dirPath)
	if !strings.HasSuffix(u, "/") {
		u += "/"
	}
	req, err := http.NewRequest("MKCOL", u, nil)
	if err != nil {
		return err
	}
	resp, err := b.do(req)
	if err != nil {
		return fmt.Errorf("mkcol: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 201 && resp.StatusCode != 405 { // 405 = already exists
		return fmt.Errorf("mkcol: status %d", resp.StatusCode)
	}
	return nil
}

func (b *webdavBackend) Delete(filePath string) error {
	req, err := http.NewRequest("DELETE", b.url(filePath), nil)
	if err != nil {
		return err
	}
	resp, err := b.do(req)
	if err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 204 && resp.StatusCode != 200 && resp.StatusCode != 404 {
		return fmt.Errorf("delete: status %d", resp.StatusCode)
	}
	return nil
}

func (b *webdavBackend) Rename(oldPath, newPath string) error {
	req, err := http.NewRequest("MOVE", b.url(oldPath), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Destination", b.url(newPath))
	req.Header.Set("Overwrite", "F")
	resp, err := b.do(req)
	if err != nil {
		return fmt.Errorf("move: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 201 && resp.StatusCode != 204 {
		return fmt.Errorf("move: status %d", resp.StatusCode)
	}
	return nil
}
