package main

const (
	defaultDBPath     = "/var/lib/birdy/birdy.db"
	defaultSocketPath = "/run/bird/bird.ctl"
	// birdy binds every interface so a fresh install is reachable without editing
	// anything. It has no TLS and its access list starts as allow-all, so the UI
	// says so until Settings → Access narrows it. Bind loopback with
	// --listen 127.0.0.1:8080 (plus an SSH tunnel) for the closed posture.
	defaultListen        = "0.0.0.0:8080"
	defaultConfigDir     = "/etc/bird"
	defaultBirdBinary    = "bird"
	defaultSystemdUnit   = "bird"
	defaultSnapshotDir   = "/var/lib/birdy/snapshots"
	defaultSnapshotKeep  = 14
	defaultBirdBackupDir = "/var/lib/birdy/bird-backups"
)
