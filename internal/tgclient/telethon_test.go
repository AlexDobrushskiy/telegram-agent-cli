package tgclient

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/gotd/td/session"

	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/output"
)

// telethonSchema is Telethon's sqlite session schema (version 8).
const telethonSchema = `
CREATE TABLE version (version integer primary key);
CREATE TABLE sessions (dc_id integer primary key, server_address text, port integer, auth_key blob, takeout_id integer, tmp_auth_key blob);
CREATE TABLE entities (id integer primary key, hash integer not null, username text, phone integer, name text, date integer);
CREATE TABLE sent_files (md5_digest blob, file_size integer, type integer, id integer, hash integer, primary key(md5_digest, file_size, type));
CREATE TABLE update_state (id integer primary key, pts integer, qts integer, date integer, seq integer);
INSERT INTO version VALUES (8);
`

// writeTelethonSession creates a synthetic session file with a random key.
func writeTelethonSession(t *testing.T, addr string, key []byte, withRow bool) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "synthetic.session")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(telethonSchema); err != nil {
		t.Fatal(err)
	}
	if withRow {
		if _, err := db.Exec(`INSERT INTO sessions VALUES (?, ?, ?, ?, NULL, ?)`, 4, addr, 443, key, []byte{}); err != nil {
			t.Fatal(err)
		}
	}
	rows := [][]any{
		{int64(424242), int64(-5555), "example_user", 15550000002, "Example User"},
		{int64(-777), int64(0), nil, nil, "Small Group"},
		{int64(-1001234567890), int64(8888), "example_channel", nil, "Example Channel"},
	}
	for _, r := range rows {
		if _, err := db.Exec(`INSERT INTO entities VALUES (?, ?, ?, ?, ?, 0)`, r...); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func randomKey(t *testing.T) []byte {
	key := make([]byte, 256)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return key
}

func TestReadTelethonSession(t *testing.T) {
	for _, addr := range []string{"192.0.2.10", "2001:db8::a"} {
		t.Run(addr, func(t *testing.T) {
			key := randomKey(t)
			imp, err := ReadTelethonSession(writeTelethonSession(t, addr, key, true))
			if err != nil {
				t.Fatal(err)
			}
			d := imp.Data
			if d.DC != 4 || !bytes.Equal(d.AuthKey, key) {
				t.Fatalf("dc=%d key match=%v", d.DC, bytes.Equal(d.AuthKey, key))
			}
			sum := sha1.Sum(key)
			if !bytes.Equal(d.AuthKeyID, sum[12:20]) {
				t.Fatal("auth key id is not the low 64 bits of SHA1(key)")
			}
			wantAddr := "192.0.2.10:443"
			if addr != "192.0.2.10" {
				wantAddr = "[2001:db8::a]:443"
			}
			if d.Addr != wantAddr {
				t.Fatalf("addr %q", d.Addr)
			}
			if len(imp.Peers) != 3 {
				t.Fatalf("peers %d", len(imp.Peers))
			}
			byID := map[int64]TelethonPeer{}
			for _, p := range imp.Peers {
				byID[p.Peer.ID] = p
			}
			u, ch := byID[424242], byID[-1001234567890]
			if u.Peer.Kind != KindUser || u.Peer.AccessHash != -5555 || u.Phone != "15550000002" || u.Peer.Username != "example_user" {
				t.Fatalf("user row %+v", u)
			}
			if ch.Peer.Kind != KindChannel || ch.Peer.RawID != 1234567890 || ch.Peer.AccessHash != 8888 {
				t.Fatalf("channel row %+v", ch)
			}
			if byID[-777].Peer.Kind != KindChat {
				t.Fatal("chat kind")
			}

			// gotd can load what we would store.
			store := &session.StorageMemory{}
			loader := session.Loader{Storage: store}
			if err := loader.Save(context.Background(), d); err != nil {
				t.Fatal(err)
			}
			back, err := loader.Load(context.Background())
			if err != nil || !bytes.Equal(back.AuthKey, key) || back.DC != 4 {
				t.Fatalf("round trip: %v", err)
			}
		})
	}
}

func TestReadTelethonSessionErrors(t *testing.T) {
	if _, err := ReadTelethonSession(writeTelethonSession(t, "192.0.2.10", randomKey(t), false)); output.As(err).Code != output.CodeUsage {
		t.Fatalf("no row: %v", err)
	}
	if _, err := ReadTelethonSession(writeTelethonSession(t, "192.0.2.10", make([]byte, 10), true)); err == nil {
		t.Fatal("short key accepted")
	}
	if _, err := ReadTelethonSession(writeTelethonSession(t, "not-an-ip", randomKey(t), true)); err == nil {
		t.Fatal("bad address accepted")
	}
	notDB := filepath.Join(t.TempDir(), "x.session")
	if _, err := ReadTelethonSession(notDB); err == nil {
		t.Fatal("missing file accepted")
	}
}
