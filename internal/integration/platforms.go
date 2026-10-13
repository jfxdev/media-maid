// Package integration holds everything Media Maid knows about media servers.
//
// platforms.go is the only file with platform-specific data. Everything else
// (sync, item listing, deletion, naming) is generic and driven by the
// PlatformSpec selected through INTEGRATION_MODE.
package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/jfxdev/media-maid/internal/naming"
)

// Endpoint is an HTTP method and path on the media server. A path may contain
// the placeholder "{id}", which is replaced with the escaped item ID.
type Endpoint struct {
	Method string
	Path   string
}

// ServerItem is a library item (movie or episode) as known by the media server.
type ServerItem struct {
	ID    string   `json:"id"`
	Type  string   `json:"type"`
	Title string   `json:"title"`
	Paths []string `json:"paths"`
}

// Requester performs an authenticated request against the configured server
// and returns the response body. It is implemented by Client.
type Requester interface {
	Request(ctx context.Context, method, path string, query url.Values) ([]byte, error)
	// Send is Request with a JSON body.
	Send(ctx context.Context, method, path string, query url.Values, body []byte) ([]byte, error)
}

// PlatformSpec declares how to talk to one kind of media server.
type PlatformSpec struct {
	Name       string
	AuthHeader string
	// IDTagFormat renders a TMDB id into the folder tag the server understands.
	IDTagFormat string
	// EditionTagFormat renders an edition label into a folder/file tag; empty
	// when the server reads editions as " - Label" versions instead.
	EditionTagFormat string
	Sync             Endpoint
	// SyncPaths refreshes only some folders (server paths); nil means the
	// platform only supports a full refresh.
	SyncPaths  func(ctx context.Context, r Requester, paths []string) error
	DeleteItem Endpoint
	// ListItems pages through the server library using the generic requester and
	// decodes the platform response into ServerItems.
	ListItems func(ctx context.Context, r Requester) ([]ServerItem, error)
	// GetItem fetches one item (already validated id) as the server knows it
	// now; ErrNotFound when it no longer exists or has no file.
	GetItem func(ctx context.Context, r Requester, id string) (ServerItem, error)
	// ExtrasDirs maps an extras folder found in a release (lower-cased, dots,
	// underscores and dashes read as spaces) to the folder the server reads
	// extras from, inside the movie or show folder. Folders it does not list
	// are never linked.
	ExtrasDirs map[string]string
}

// ExtrasDir returns the library folder for a release's extras folder.
func (s PlatformSpec) ExtrasDir(category string) (string, bool) {
	d, ok := s.ExtrasDirs[category]
	return d, ok
}

// embyExtras are the extras folders Emby reads; clips go to the generic
// Extras folder.
var embyExtras = map[string]string{
	"extras": "Extras", "bonus": "Extras", "clips": "Extras", "featurettes": "Featurettes", "trailers": "Trailers",
	"behind the scenes": "Behind The Scenes", "deleted scenes": "Deleted Scenes",
	"interviews": "Interviews", "scenes": "Scenes", "shorts": "Shorts",
}

// jellyfinExtras adds the Clips folder.
// https://jellyfin.org/docs/general/server/media/movies/#extras
var jellyfinExtras = withExtra(embyExtras, "clips", "Clips")

func withExtra(m map[string]string, k, v string) map[string]string {
	out := make(map[string]string, len(m)+1)
	for a, b := range m {
		out[a] = b
	}
	out[k] = v
	return out
}

// IDTag returns the folder/file tag for a TMDB id, or "" when id is zero.
func (s PlatformSpec) IDTag(tmdbID int) string {
	if tmdbID <= 0 {
		return ""
	}
	return fmt.Sprintf(s.IDTagFormat, tmdbID)
}

// EditionTag returns the tag for an edition label, or "" when the server has no
// edition tag or label is empty. The label is sanitized by the caller's
// naming rules first, so only the tag's own braces are added.
func (s PlatformSpec) EditionTag(label string) string {
	if s.EditionTagFormat == "" || label == "" {
		return ""
	}
	return fmt.Sprintf(s.EditionTagFormat, naming.Sanitize(label))
}

const pageSize = 500

var platforms = map[string]PlatformSpec{
	"jellyfin": {
		Name:        "jellyfin",
		AuthHeader:  "X-Emby-Token",
		IDTagFormat: "[tmdbid-%d]",
		Sync:        Endpoint{http.MethodPost, "/Library/Refresh"},
		SyncPaths:   mediaUpdated("/Library/Media/Updated"),
		DeleteItem:  Endpoint{http.MethodDelete, "/Items/{id}"},
		ListItems:   listEmbyStyle("/Items"),
		GetItem:     getEmbyStyle("/Items"),
		ExtrasDirs:  jellyfinExtras,
	},
	"emby": {
		Name:        "emby",
		AuthHeader:  "X-Emby-Token",
		IDTagFormat: "[tmdbid=%d]",
		Sync:        Endpoint{http.MethodPost, "/emby/Library/Refresh"},
		SyncPaths:   mediaUpdated("/emby/Library/Media/Updated"),
		DeleteItem:  Endpoint{http.MethodDelete, "/emby/Items/{id}"},
		ListItems:   listEmbyStyle("/emby/Items"),
		GetItem:     getEmbyStyle("/emby/Items"),
		ExtrasDirs:  embyExtras,
	},
	"plex": {
		Name:        "plex",
		AuthHeader:  "X-Plex-Token",
		IDTagFormat: "{tmdb-%d}",
		// https://support.plex.tv/articles/multiple-editions/
		EditionTagFormat: "{edition-%s}",
		Sync:             Endpoint{http.MethodGet, "/library/sections/all/refresh"},
		SyncPaths:        plexRefreshPaths,
		DeleteItem:       Endpoint{http.MethodDelete, "/library/metadata/{id}"},
		ListItems:        listPlex,
		GetItem:          getPlex,
		// https://support.plex.tv/articles/local-files-for-trailers-and-extras/
		ExtrasDirs: map[string]string{
			"extras": "Other", "bonus": "Other", "clips": "Other", "featurettes": "Featurettes",
			"trailers": "Trailers", "behind the scenes": "Behind The Scenes",
			"deleted scenes": "Deleted Scenes", "interviews": "Interviews", "scenes": "Scenes",
			"shorts": "Shorts",
		},
	},
}

// Lookup returns the spec for an integration mode.
func Lookup(mode string) (PlatformSpec, bool) {
	s, ok := platforms[mode]
	return s, ok
}

// Modes returns the supported integration modes, sorted.
func Modes() []string {
	out := make([]string, 0, len(platforms))
	for k := range platforms {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// mediaUpdated reports changed folders to Jellyfin/Emby, which then scan
// only those ("Library/Media/Updated").
func mediaUpdated(endpoint string) func(context.Context, Requester, []string) error {
	return func(ctx context.Context, r Requester, paths []string) error {
		type update struct {
			Path       string `json:"Path"`
			UpdateType string `json:"UpdateType"`
		}
		var body struct {
			Updates []update `json:"Updates"`
		}
		for _, p := range paths {
			body.Updates = append(body.Updates, update{Path: p, UpdateType: "Created"})
		}
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		_, err = r.Send(ctx, http.MethodPost, endpoint, nil, data)
		return err
	}
}

// plexRefreshPaths refreshes each folder in the Plex section whose location
// contains it ("sections/{id}/refresh?path=").
func plexRefreshPaths(ctx context.Context, r Requester, paths []string) error {
	body, err := r.Request(ctx, http.MethodGet, "/library/sections", nil)
	if err != nil {
		return err
	}
	var sections struct {
		MediaContainer struct {
			Directory []struct {
				Key      string `json:"key"`
				Location []struct {
					Path string `json:"path"`
				} `json:"Location"`
			} `json:"Directory"`
		} `json:"MediaContainer"`
	}
	if err := json.Unmarshal(body, &sections); err != nil {
		return fmt.Errorf("decode sections: %w", err)
	}
	keys := make([]string, len(paths))
	for i, p := range paths {
		for _, d := range sections.MediaContainer.Directory {
			for _, loc := range d.Location {
				if loc.Path != "" && within(path.Clean(loc.Path), path.Clean(p)) {
					keys[i] = d.Key
				}
			}
		}
		if keys[i] == "" {
			return ErrNoTargetedSync
		}
	}
	for i, p := range paths {
		q := url.Values{"path": {p}}
		if _, err := r.Request(ctx, http.MethodGet, "/library/sections/"+url.PathEscape(keys[i])+"/refresh", q); err != nil {
			return err
		}
	}
	return nil
}

// within reports whether p is root or inside it (slash-separated paths).
func within(root, p string) bool {
	return p == root || strings.HasPrefix(p, strings.TrimSuffix(root, "/")+"/")
}

// listEmbyStyle lists movies and episodes from Jellyfin/Emby's /Items endpoint.
func listEmbyStyle(path string) func(context.Context, Requester) ([]ServerItem, error) {
	return func(ctx context.Context, r Requester) ([]ServerItem, error) {
		var out []ServerItem
		for start := 0; ; start += pageSize {
			q := url.Values{
				"Recursive":        {"true"},
				"IncludeItemTypes": {"Movie,Episode"},
				"Fields":           {"Path"},
				"StartIndex":       {strconv.Itoa(start)},
				"Limit":            {strconv.Itoa(pageSize)},
			}
			body, err := r.Request(ctx, http.MethodGet, path, q)
			if err != nil {
				return nil, err
			}
			items, n, total, err := decodeEmbyItems(body)
			if err != nil {
				return nil, err
			}
			out = append(out, items...)
			if n < pageSize || start+n >= total {
				return out, nil
			}
		}
	}
}

// decodeEmbyItems decodes a Jellyfin/Emby /Items page: the items with a path,
// how many items the page held and the server's total.
func decodeEmbyItems(body []byte) (items []ServerItem, n, total int, err error) {
	var page struct {
		Items []struct {
			ID   string `json:"Id"`
			Name string `json:"Name"`
			Type string `json:"Type"`
			Path string `json:"Path"`
		} `json:"Items"`
		TotalRecordCount int `json:"TotalRecordCount"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, 0, 0, fmt.Errorf("decode items: %w", err)
	}
	for _, it := range page.Items {
		if it.Path == "" {
			continue
		}
		items = append(items, ServerItem{ID: it.ID, Type: it.Type, Title: it.Name, Paths: []string{it.Path}})
	}
	return items, len(page.Items), page.TotalRecordCount, nil
}

// getEmbyStyle fetches one item from Jellyfin/Emby's /Items endpoint.
func getEmbyStyle(path string) func(context.Context, Requester, string) (ServerItem, error) {
	return func(ctx context.Context, r Requester, id string) (ServerItem, error) {
		q := url.Values{"Ids": {id}, "Recursive": {"true"}, "Fields": {"Path"}}
		body, err := r.Request(ctx, http.MethodGet, path, q)
		if err != nil {
			return ServerItem{}, err
		}
		items, _, _, err := decodeEmbyItems(body)
		if err != nil {
			return ServerItem{}, err
		}
		return findItem(items, id)
	}
}

// findItem returns the item with id, or ErrNotFound.
func findItem(items []ServerItem, id string) (ServerItem, error) {
	for _, it := range items {
		if it.ID == id {
			return it, nil
		}
	}
	return ServerItem{}, ErrNotFound
}

// listPlex lists movies (type=1) and episodes (type=4) from every Plex section.
func listPlex(ctx context.Context, r Requester) ([]ServerItem, error) {
	body, err := r.Request(ctx, http.MethodGet, "/library/sections", nil)
	if err != nil {
		return nil, err
	}
	var sections struct {
		MediaContainer struct {
			Directory []struct {
				Key  string `json:"key"`
				Type string `json:"type"`
			} `json:"Directory"`
		} `json:"MediaContainer"`
	}
	if err := json.Unmarshal(body, &sections); err != nil {
		return nil, fmt.Errorf("decode sections: %w", err)
	}
	var out []ServerItem
	for _, sec := range sections.MediaContainer.Directory {
		var typ string
		switch sec.Type {
		case "movie":
			typ = "1"
		case "show":
			typ = "4"
		default:
			continue
		}
		for start := 0; ; start += pageSize {
			q := url.Values{
				"type":                   {typ},
				"X-Plex-Container-Start": {strconv.Itoa(start)},
				"X-Plex-Container-Size":  {strconv.Itoa(pageSize)},
			}
			body, err := r.Request(ctx, http.MethodGet, "/library/sections/"+url.PathEscape(sec.Key)+"/all", q)
			if err != nil {
				return nil, err
			}
			items, n, total, err := decodePlexMetadata(body)
			if err != nil {
				return nil, err
			}
			out = append(out, items...)
			if n < pageSize || (total > 0 && start+n >= total) {
				break
			}
		}
	}
	return out, nil
}

// decodePlexMetadata decodes a Plex MediaContainer of items: those with at
// least one file, how many the page held and the server's total.
func decodePlexMetadata(body []byte) (items []ServerItem, n, total int, err error) {
	var page struct {
		MediaContainer struct {
			TotalSize int `json:"totalSize"`
			Metadata  []struct {
				RatingKey string `json:"ratingKey"`
				Title     string `json:"title"`
				Type      string `json:"type"`
				Media     []struct {
					Part []struct {
						File string `json:"file"`
					} `json:"Part"`
				} `json:"Media"`
			} `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, 0, 0, fmt.Errorf("decode section items: %w", err)
	}
	for _, md := range page.MediaContainer.Metadata {
		item := ServerItem{ID: md.RatingKey, Type: md.Type, Title: md.Title}
		for _, m := range md.Media {
			for _, p := range m.Part {
				if p.File != "" {
					item.Paths = append(item.Paths, p.File)
				}
			}
		}
		if len(item.Paths) > 0 {
			items = append(items, item)
		}
	}
	return items, len(page.MediaContainer.Metadata), page.MediaContainer.TotalSize, nil
}

// getPlex fetches one item from /library/metadata/{id}.
func getPlex(ctx context.Context, r Requester, id string) (ServerItem, error) {
	body, err := r.Request(ctx, http.MethodGet, "/library/metadata/"+url.PathEscape(id), nil)
	if err != nil {
		return ServerItem{}, err
	}
	items, _, _, err := decodePlexMetadata(body)
	if err != nil {
		return ServerItem{}, err
	}
	return findItem(items, id)
}
