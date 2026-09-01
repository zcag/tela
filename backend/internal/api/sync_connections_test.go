package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

type syncConnResp struct {
	Connection struct {
		ID      int64  `json:"id"`
		Scope   string `json:"scope"`
		SpaceID *int64 `json:"space_id"`
		Key     string `json:"key"`
	} `json:"connection"`
	Rclone struct {
		RemotePath          string `json:"remote_path"`
		ConfigCreateCommand string `json:"config_create_command"`
		MountCommand        string `json:"mount_command"`
		MountCommandMacos   string `json:"mount_command_macos"`
		ServiceName         string `json:"service_name"`
		SystemdUnit         string `json:"systemd_unit"`
		LaunchdPlist        string `json:"launchd_plist"`
	} `json:"rclone"`
}

func TestSyncConnections_CreateListRevoke(t *testing.T) {
	ts, d := newWiredServer(t)
	uid := seedUser(t, d, "member", "memberpw12", false)
	space := seedSpace(t, d, "Engineering", "engineering", uid)
	other := seedSpace(t, d, "Secret", "secret", 0) // user is NOT a member
	c := loginClient(t, ts, "member", "memberpw12")

	// Space-pinned write connection.
	resp, _ := postJSON(c, ts.URL+"/api/sync/connections", fmt.Sprintf(`{"name":"laptop","space_id":%d}`, space))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d, want 201", resp.StatusCode)
	}
	var got syncConnResp
	json.NewDecoder(resp.Body).Decode(&got)
	resp.Body.Close()
	if !strings.HasPrefix(got.Connection.Key, "tela_pat_") {
		t.Fatalf("missing raw key: %q", got.Connection.Key)
	}
	if got.Connection.Scope != "write" || got.Connection.SpaceID == nil || *got.Connection.SpaceID != space {
		t.Fatalf("connection scope/space wrong: %+v", got.Connection)
	}
	if !strings.Contains(got.Rclone.ConfigCreateCommand, got.Connection.Key) {
		t.Fatalf("config command missing the key:\n%s", got.Rclone.ConfigCreateCommand)
	}
	// --obscure is load-bearing: without it rclone stores the (revealable) PAT raw
	// and de-obscures it into garbage on every request → 401. See buildRcloneSetup.
	if !strings.Contains(got.Rclone.ConfigCreateCommand, "--obscure") {
		t.Fatalf("config command must pass --obscure or rclone stores the PAT raw → 401:\n%s", got.Rclone.ConfigCreateCommand)
	}
	// vendor=rclone is what makes the client send/read a modtime (X-OC-Mtime).
	// With vendor=other and the mandatory --ignore-size, rclone has no comparison
	// signal left and silently skips every edit to an already-synced file.
	if !strings.Contains(got.Rclone.ConfigCreateCommand, "vendor=rclone") {
		t.Fatalf("config command must set vendor=rclone or edits never upload:\n%s", got.Rclone.ConfigCreateCommand)
	}
	if !strings.Contains(got.Rclone.MountCommand, "rclone mount") || !strings.Contains(got.Rclone.MountCommand, "--ignore-size") {
		t.Fatalf("mount command should be `rclone mount … --ignore-size`:\n%s", got.Rclone.MountCommand)
	}
	// macOS has no FUSE, so its mount is nfsmount and its keep-alive is launchd.
	if !strings.Contains(got.Rclone.MountCommandMacos, "rclone nfsmount") {
		t.Fatalf("macOS mount command should be `rclone nfsmount …`:\n%s", got.Rclone.MountCommandMacos)
	}
	if !strings.Contains(got.Rclone.LaunchdPlist, "nfsmount") || strings.Contains(got.Rclone.LaunchdPlist, "%h") {
		t.Fatalf("launchd plist should run nfsmount and expand $HOME itself:\n%s", got.Rclone.LaunchdPlist)
	}
	if !strings.HasSuffix(got.Rclone.RemotePath, ":engineering") {
		t.Fatalf("remote path not scoped to the space slug: %q", got.Rclone.RemotePath)
	}
	if got.Rclone.ServiceName != "tela-engineering" {
		t.Fatalf("service name = %q, want tela-engineering", got.Rclone.ServiceName)
	}
	if !strings.Contains(got.Rclone.SystemdUnit, "ExecStart=/usr/bin/rclone mount "+got.Rclone.RemotePath) {
		t.Fatalf("systemd unit missing the mount ExecStart:\n%s", got.Rclone.SystemdUnit)
	}

	// Read-only, whole-workspace connection.
	resp, _ = postJSON(c, ts.URL+"/api/sync/connections", `{"name":"phone","read_only":true}`)
	var ro syncConnResp
	json.NewDecoder(resp.Body).Decode(&ro)
	resp.Body.Close()
	if ro.Connection.Scope != "read" || ro.Connection.SpaceID != nil {
		t.Fatalf("read-only workspace connection wrong: %+v", ro.Connection)
	}
	if ro.Rclone.RemotePath != "tela:" {
		t.Fatalf("workspace remote path = %q, want tela:", ro.Rclone.RemotePath)
	}
	if ro.Rclone.ServiceName != "tela-vault" {
		t.Fatalf("workspace service name = %q, want tela-vault", ro.Rclone.ServiceName)
	}
	if !strings.Contains(ro.Rclone.MountCommand, "--read-only") {
		t.Fatalf("read-only connection should mount --read-only:\n%s", ro.Rclone.MountCommand)
	}

	// Pinning to a space the user can't access is refused.
	resp, _ = postJSON(c, ts.URL+"/api/sync/connections", fmt.Sprintf(`{"name":"x","space_id":%d}`, other))
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("pin to non-member space = %d, want 403", resp.StatusCode)
	}
	resp.Body.Close()

	// List shows the caller's two connections, without the raw key.
	resp, _ = c.Get(ts.URL + "/api/sync/connections")
	var list struct {
		Connections []struct {
			Key string `json:"key"`
		} `json:"connections"`
	}
	json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if len(list.Connections) != 2 {
		t.Fatalf("list returned %d connections, want 2", len(list.Connections))
	}
	for _, conn := range list.Connections {
		if conn.Key != "" {
			t.Fatal("list must never re-expose the raw key")
		}
	}

	// Owner self-revoke via the shared api_keys delete.
	req, _ := http.NewRequest("DELETE", fmt.Sprintf("%s/api/api_keys/%d", ts.URL, got.Connection.ID), nil)
	if r, _ := c.Do(req); r.StatusCode != http.StatusNoContent {
		t.Fatalf("self-revoke = %d, want 204", r.StatusCode)
	}
}
