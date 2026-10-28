package tgclient

import (
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"net"
	"net/url"
	"path/filepath"

	"github.com/gotd/td/session"
	_ "modernc.org/sqlite" // pure-Go SQLite driver

	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/output"
)

// TelethonImport is what a Telethon SQLite session yields.
type TelethonImport struct {
	Data  *session.Data
	Peers []TelethonPeer
}

// TelethonPeer is one row of Telethon's entities table (marked id + access hash).
type TelethonPeer struct {
	Peer  Peer
	Phone string
}

// ReadTelethonSession reads the `sessions` row (dc_id, server_address, port, auth_key)
// and the `entities` table of a Telethon .session file, read-only.
func ReadTelethonSession(path string) (*TelethonImport, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	dsn := (&url.URL{Scheme: "file", Path: abs, RawQuery: "mode=ro"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	var (
		dc   int
		addr string
		port int
		key  []byte
	)
	err = db.QueryRow(`SELECT dc_id, server_address, port, auth_key FROM sessions LIMIT 1`).Scan(&dc, &addr, &port, &key)
	if err == sql.ErrNoRows {
		return nil, output.Usage("%s has no session row (never logged in?)", path)
	}
	if err != nil {
		return nil, output.Usage("%s is not a Telethon session: %v", path, err)
	}
	data, err := telethonData(dc, addr, port, key)
	if err != nil {
		return nil, output.Usage("%s: %v", path, err)
	}
	out := &TelethonImport{Data: data}

	rows, err := db.Query(`SELECT id, hash, username, phone, name FROM entities`)
	if err != nil {
		return out, nil // old or stripped sessions have no entities table
	}
	defer rows.Close()
	for rows.Next() {
		var id, hash int64
		var username, phone, name sql.NullString
		if rows.Scan(&id, &hash, &username, &phone, &name) != nil || id == 0 {
			continue
		}
		kind, raw := SplitMarked(id)
		out.Peers = append(out.Peers, TelethonPeer{
			Peer:  Peer{ID: id, Kind: kind, RawID: raw, AccessHash: hash, Name: name.String, Username: username.String},
			Phone: phone.String,
		})
	}
	return out, rows.Err()
}

// telethonData packs the row as a Telethon string session and decodes it with gotd.
func telethonData(dc int, addr string, port int, key []byte) (*session.Data, error) {
	if dc <= 0 || dc > 255 {
		return nil, fmt.Errorf("bad dc_id %d", dc)
	}
	if len(key) != 256 {
		return nil, fmt.Errorf("auth_key is %d bytes, want 256", len(key))
	}
	if port <= 0 || port > 65535 {
		return nil, fmt.Errorf("bad port %d", port)
	}
	ip := net.ParseIP(addr)
	if ip == nil {
		return nil, fmt.Errorf("bad server_address %q", addr)
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	buf := make([]byte, 0, 1+len(ip)+2+len(key))
	buf = append(buf, byte(dc))
	buf = append(buf, ip...)
	buf = binary.BigEndian.AppendUint16(buf, uint16(port))
	buf = append(buf, key...)
	return session.TelethonSession("1" + base64.URLEncoding.EncodeToString(buf))
}
