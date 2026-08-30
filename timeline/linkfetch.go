/*
	Timelinize
	Copyright (c) 2013 Matthew Holt

	This program is free software: you can redistribute it and/or modify
	it under the terms of the GNU Affero General Public License as published
	by the Free Software Foundation, either version 3 of the License, or
	(at your option) any later version.

	This program is distributed in the hope that it will be useful,
	but WITHOUT ANY WARRANTY; without even the implied warranty of
	MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
	GNU Affero General Public License for more details.

	You should have received a copy of the GNU Affero General Public License
	along with this program.  If not, see <https://www.gnu.org/licenses/>.
*/

package timeline

// LinkFetchOptions configures the optional resolution of links that data sources
// encounter (e.g. reels/posts shared in DMs): downloading the linked media with
// external tools (yt-dlp, gallery-dl) using the user's own cookies. Disabled by
// default; nothing is ever fetched unless Enabled is true.
//
// Can be set per import job (processing_options.link_fetch) with defaults coming
// from the app config (link_fetch). See docs/fork/link-fetching.md.
type LinkFetchOptions struct {
	Enabled bool `json:"enabled,omitempty"`

	// Netscape cookies.txt files keyed by data source name (e.g. "instagram", "facebook").
	Cookies map[string]string `json:"cookies,omitempty"`

	// Minimum delay between two network fetches (default 3000).
	DelayMS int `json:"delay_ms,omitempty"`

	// Maximum number of network fetches per import; 0 = unlimited. Cache hits are free.
	MaxPerImport int `json:"max_per_import,omitempty"`

	// Timeout for a single fetch (default 120).
	TimeoutSec int `json:"timeout_s,omitempty"`

	// Where results and downloaded files are cached across imports.
	// Default: <repo>/linkfetch.
	CacheDir string `json:"cache_dir,omitempty"`

	// How many times a failed URL is retried on later imports (default 3).
	MaxAttempts int `json:"max_attempts,omitempty"`

	// Paths of the external tools, if not on PATH.
	YTDLPPath     string `json:"ytdlp_path,omitempty"`
	GalleryDLPath string `json:"gallerydl_path,omitempty"`
}

// FillDefaults copies any unset field from def (which may be nil).
func (o *LinkFetchOptions) FillDefaults(def *LinkFetchOptions) {
	if o == nil || def == nil {
		return
	}
	if len(o.Cookies) == 0 {
		o.Cookies = def.Cookies
	}
	if o.DelayMS == 0 {
		o.DelayMS = def.DelayMS
	}
	if o.MaxPerImport == 0 {
		o.MaxPerImport = def.MaxPerImport
	}
	if o.TimeoutSec == 0 {
		o.TimeoutSec = def.TimeoutSec
	}
	if o.CacheDir == "" {
		o.CacheDir = def.CacheDir
	}
	if o.MaxAttempts == 0 {
		o.MaxAttempts = def.MaxAttempts
	}
	if o.YTDLPPath == "" {
		o.YTDLPPath = def.YTDLPPath
	}
	if o.GalleryDLPath == "" {
		o.GalleryDLPath = def.GalleryDLPath
	}
}
