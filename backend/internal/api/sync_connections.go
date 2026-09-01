package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/zcag/tela/backend/internal/auth"
)

// sync_connections.go — user self-service "Connect your vault" (sync §16). A
// member mints a WebDAV sync token for THEIR OWN access (an api_keys row owned
// by them, write or read-only, optionally pinned to one space they belong to)
// and gets a ready-to-paste rclone setup. Distinct from the /api/api_keys CRUD,
// which is instance-admin-only for issuing agent keys: this surface is
// space-membership-gated personal access, the one the app's Sync settings panel
// drives. Revoke reuses the owner-gated DELETE /api/api_keys/{id}.

type syncConnectionCreateRequest struct {
	Name     string `json:"name"`
	SpaceID  *int64 `json:"space_id"`  // nil → whole workspace
	ReadOnly bool   `json:"read_only"` // false → write (two-way); true → sync-down only
}

// rcloneSetup is the copy-paste configuration the panel shows once. The raw PAT
// is embedded in config_create_command (same once-only exposure as api_keys).
type rcloneSetup struct {
	WebdavURL  string `json:"webdav_url"`
	RemoteName string `json:"remote_name"`
	RemotePath string `json:"remote_path"`
	// LocalDir is the concrete folder the vault is mounted at (~-anchored, so the
	// user always knows WHERE), e.g. ~/tela or ~/tela/<slug>.
	LocalDir            string `json:"local_dir"`
	ConfigCreateCommand string `json:"config_create_command"` // step 1, run once
	MountCommand        string `json:"mount_command"`         // step 2: foreground mount to try it
	MountCommandMacos   string `json:"mount_command_macos"`   // step 2 on macOS: nfsmount, no FUSE kext
	ServiceName         string `json:"service_name"`          // the systemd unit / launchd label stem
	SystemdUnit         string `json:"systemd_unit"`          // step 3 (Linux): the .service file contents
	SystemdInstall      string `json:"systemd_install"`       // step 3 (Linux): enable + start it
	LaunchdLabel        string `json:"launchd_label"`         // step 3 (macOS): the agent label / plist name
	LaunchdPlist        string `json:"launchd_plist"`         // step 3 (macOS): the .plist contents
	LaunchdInstall      string `json:"launchd_install"`       // step 3 (macOS): load it
	// FixVendorCommand repairs a remote created before tela had modtime support
	// (vendor=other). Without vendor=rclone the client sends no X-OC-Mtime and
	// ignores ours, so rclone cannot tell an edited file from an unchanged one.
	FixVendorCommand string `json:"fix_vendor_command"`
	ReadOnly         bool   `json:"read_only"`
}

// CreateSyncConnection — POST /api/sync/connections. Any authenticated user mints
// a sync token for their own access; a space pin is gated on membership.
func (s *Server) CreateSyncConnection(w http.ResponseWriter, r *http.Request) {
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	var req syncConnectionCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "could not parse request body")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len(name) > apiKeyMaxNameLen {
		writeError(w, http.StatusBadRequest, "bad_request", "name must be 1-100 characters")
		return
	}
	scope := auth.ScopeWrite
	if req.ReadOnly {
		scope = auth.ScopeRead
	}

	ctx := r.Context()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "begin tx failed")
		return
	}
	defer tx.Rollback()

	// A space pin must name a space the caller belongs to — the token can't be
	// scoped to access the user doesn't have.
	var (
		spaceArg  any = nil
		spaceSlug string
	)
	if req.SpaceID != nil {
		if *req.SpaceID <= 0 {
			writeError(w, http.StatusBadRequest, "bad_request", "space_id must be a positive integer")
			return
		}
		if _, err := spaceRoleTx(ctx, tx, u.ID, *req.SpaceID); errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusForbidden, "forbidden", "not a member of that space")
			return
		} else if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "lookup membership failed")
			return
		}
		if err := tx.QueryRowContext(ctx, `SELECT slug FROM spaces WHERE id = $1`, *req.SpaceID).Scan(&spaceSlug); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "lookup space failed")
			return
		}
		spaceArg = *req.SpaceID
	}

	raw, prefix, hmacHex, err := auth.NewAPIKey(auth.LoadAPIKeySecret())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "generate token failed")
		return
	}
	var id int64
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO api_keys (user_id, name, key_prefix, key_hmac, scope, space_id)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		u.ID, name, prefix, hmacHex, scope, spaceArg).Scan(&id); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "insert token failed")
		return
	}
	dto, err := selectAPIKeyByIDTx(ctx, tx, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "fetch created token failed")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "commit failed")
		return
	}
	dto.Key = raw
	writeJSON(w, http.StatusCreated, map[string]any{
		"connection": dto,
		"rclone":     buildRcloneSetup(firstNonEmpty(u.Email, u.Username), spaceSlug, raw, req.ReadOnly),
	})
}

// ListSyncConnections — GET /api/sync/connections. The caller's own sync tokens
// (revoked rows included so the history is visible); never re-exposes the key.
func (s *Server) ListSyncConnections(w http.ResponseWriter, r *http.Request) {
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	rows, err := s.DB.QueryContext(r.Context(),
		apiKeySelectColumns+` WHERE user_id = $1 ORDER BY id DESC`, u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "list connections failed")
		return
	}
	defer rows.Close()
	out := []apiKeyDTO{}
	for rows.Next() {
		dto, err := scanAPIKey(rows)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "scan connection row failed")
			return
		}
		out = append(out, dto)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "iterate connections failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"connections": out})
}

// buildRcloneSetup assembles the copy-paste rclone configuration for a freshly
// minted token. The WebDAV URL, the vendor and the --ignore-size flag (tela
// transforms files on write, so rclone must not size-check — see
// docs/webdav-sync.md) are server-authoritative, not hardcoded in the client.
func buildRcloneSetup(user, spaceSlug, rawKey string, readOnly bool) rcloneSetup {
	url := strings.TrimRight(canonicalBaseURL(), "/") + "/dav/"
	const remote = "tela"
	remotePath := remote + ":"
	localRel := "tela" // relative to $HOME
	service := "tela-vault"
	if spaceSlug != "" {
		remotePath = remote + ":" + spaceSlug
		localRel = "tela/" + spaceSlug
		service = "tela-" + spaceSlug
	}
	localDir := "~/" + localRel
	if user == "" {
		user = "you@example.com"
	}
	// --obscure is REQUIRED, not cosmetic: rclone stores webdav passwords obscured
	// and de-obscures them before sending. Without the flag, rclone first tries to
	// reveal the value to guess if it's "already obscured" — and a tela PAT happens
	// to be a revealable string, so rclone assumes it's pre-obscured and stores it
	// RAW. It then de-obscures the raw token into garbage on every request → 401.
	// --obscure forces obscuring so the round-trip is correct.
	// vendor=rclone is LOAD-BEARING, not cosmetic. It is the profile for a WebDAV
	// server that accepts an upload modtime via X-OC-Mtime (dav_mtime.go) — with
	// it rclone reports Precision: 1s and compares modtimes; with vendor=other it
	// reports ModTimeNotSupported, and since --ignore-size is mandatory here that
	// leaves rclone with NO comparison signal at all: its equal() then returns
	// "Sizes identical" for every file that already exists and silently skips
	// every edit. It picks up nothing else from the vendor (no chunking, no
	// checksum properties, no propset).
	configCreate := fmt.Sprintf("rclone config create %s webdav url=%s vendor=rclone user=%s pass=%s --obscure", remote, url, user, rawKey)
	fixVendor := fmt.Sprintf("rclone config update %s vendor rclone", remote)

	// The vault is mounted (rclone mount), not bidirectionally synced: edits go
	// straight up via PUT (the server merges), server-side changes appear after
	// --dir-cache-time. Required flags, verified against tela's WebDAV:
	//   --ignore-size : tela renders frontmatter on write, so the stored bytes
	//                   differ from what was uploaded; without this the VFS
	//                   declares "corrupted on transfer" and retries forever.
	//                   (modtime stays on — getlastmodified=updated_at — so
	//                   server→local change detection still works.)
	//   --vfs-cache-mode full : real local cache, so editors and offline reads work.
	// Read-only tokens add --read-only.
	roFlag := ""
	if readOnly {
		roFlag = " --read-only"
	}
	mountFlags := "--vfs-cache-mode full --dir-cache-time 10s --vfs-write-back 2s --ignore-size" + roFlag
	mountCmd := fmt.Sprintf("rclone mount %s %s %s", remotePath, localDir, mountFlags)
	// macOS has no FUSE (macFUSE is a kext needing Reduced Security + a reboot),
	// so `rclone mount` is a non-starter there; nfsmount serves the same tree over
	// a loopback NFS mount with no kext and no root.
	mountCmdMac := fmt.Sprintf("rclone nfsmount %s %s %s", remotePath, localDir, mountFlags)

	// systemd USER service — mounts on login, restarts if it drops (the pattern
	// for an always-available cloud folder). %h is systemd's user-home specifier
	// (the unit can't carry a real path; it's generated server-side).
	unit := strings.NewReplacer(
		"{remote}", remotePath,
		"{rel}", localRel,
		"{flags}", mountFlags,
	).Replace(`[Unit]
Description=tela vault — rclone mount of {remote}
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStartPre=/usr/bin/mkdir -p %h/{rel}
ExecStartPre=-/usr/bin/fusermount3 -uz %h/{rel}
ExecStart=/usr/bin/rclone mount {remote} %h/{rel} {flags}
ExecStop=/usr/bin/fusermount3 -uz %h/{rel}
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
`)
	install := fmt.Sprintf("systemctl --user daemon-reload\nsystemctl --user enable --now %s.service", service)

	// launchd agent — the macOS half of "keep it mounted". The command runs under
	// /bin/sh so $HOME expands on the user's machine (a plist has no %h, and we
	// must not bake a username in), and PATH is set explicitly because a launchd
	// agent inherits none — Homebrew lives in /opt/homebrew/bin (Apple silicon)
	// or /usr/local/bin (Intel).
	label := "com.telawiki." + service
	plist := strings.NewReplacer(
		"{label}", label,
		"{remote}", remotePath,
		"{rel}", localRel,
		"{flags}", mountFlags,
	).Replace(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>{label}</string>
  <key>ProgramArguments</key>
  <array>
    <string>/bin/sh</string>
    <string>-c</string>
    <string>mkdir -p "$HOME/{rel}" &amp;&amp; exec rclone nfsmount {remote} "$HOME/{rel}" {flags}</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict><key>PATH</key><string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin</string></dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardErrorPath</key><string>/tmp/{label}.log</string>
</dict>
</plist>
`)
	launchdInstall := fmt.Sprintf("launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/%s.plist", label)

	return rcloneSetup{
		WebdavURL:           url,
		RemoteName:          remote,
		RemotePath:          remotePath,
		LocalDir:            localDir,
		ConfigCreateCommand: configCreate,
		MountCommand:        mountCmd,
		MountCommandMacos:   mountCmdMac,
		ServiceName:         service,
		SystemdUnit:         unit,
		SystemdInstall:      install,
		LaunchdLabel:        label,
		LaunchdPlist:        plist,
		LaunchdInstall:      launchdInstall,
		FixVendorCommand:    fixVendor,
		ReadOnly:            readOnly,
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
