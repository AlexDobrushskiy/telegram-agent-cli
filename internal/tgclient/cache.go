package tgclient

import (
	"encoding/json"
	"os"
	"strconv"
)

// PeerCache remembers access hashes so numeric ids resolve without walking every dialog.
// It is a cache: a missing or corrupt file just starts empty.
type PeerCache struct {
	path  string
	peers map[int64]cacheEntry
	dirty bool
}

type cacheEntry struct {
	Kind       Kind   `json:"kind"`
	ID         int64  `json:"id"`
	AccessHash int64  `json:"access_hash,omitempty"`
	Name       string `json:"name,omitempty"`
	Username   string `json:"username,omitempty"`
	Phone      string `json:"phone,omitempty"`
	Type       string `json:"type,omitempty"`
}

type cacheFile struct {
	Version int                   `json:"version"`
	Peers   map[string]cacheEntry `json:"peers"`
}

const cacheVersion = 1

// LoadPeerCache reads path; errors yield an empty cache.
func LoadPeerCache(path string) *PeerCache {
	c := &PeerCache{path: path, peers: map[int64]cacheEntry{}}
	b, err := os.ReadFile(path)
	if err != nil {
		return c
	}
	var f cacheFile
	if json.Unmarshal(b, &f) != nil || f.Version != cacheVersion {
		return c
	}
	for k, v := range f.Peers {
		if id, err := strconv.ParseInt(k, 10, 64); err == nil {
			c.peers[id] = v
		}
	}
	return c
}

// Len is the number of cached peers.
func (c *PeerCache) Len() int { return len(c.peers) }

// Get returns a cached peer by marked id.
func (c *PeerCache) Get(id int64) (Peer, bool) {
	e, ok := c.peers[id]
	if !ok {
		return Peer{}, false
	}
	return e.peer(id), true
}

func (e cacheEntry) peer(id int64) Peer {
	return Peer{ID: id, Kind: e.Kind, RawID: e.ID, AccessHash: e.AccessHash, Name: e.Name, Type: e.Type, Username: e.Username}
}

// Put stores p. A min access hash never replaces a known full one.
func (c *PeerCache) Put(p Peer, phone string, min bool) {
	old, had := c.peers[p.ID]
	e := cacheEntry{Kind: p.Kind, ID: p.RawID, AccessHash: p.AccessHash, Name: p.Name, Username: p.Username, Phone: phone, Type: p.Type}
	if had {
		if min || e.AccessHash == 0 {
			e.AccessHash = old.AccessHash
		}
		if e.Phone == "" {
			e.Phone = old.Phone
		}
		if e.Type == "" {
			e.Type = old.Type
		}
	} else if min {
		return
	}
	if had && old == e {
		return
	}
	c.peers[p.ID] = e
	c.dirty = true
}

// FindPhone looks a user up by phone digits.
func (c *PeerCache) FindPhone(digits string) (Peer, bool) {
	for id, e := range c.peers {
		if e.Phone != "" && e.Phone == digits {
			return e.peer(id), true
		}
	}
	return Peer{}, false
}

// Save writes the cache with write (an atomic 0600 writer) if it changed.
func (c *PeerCache) Save(write func(path string, data []byte) error) error {
	if !c.dirty {
		return nil
	}
	f := cacheFile{Version: cacheVersion, Peers: make(map[string]cacheEntry, len(c.peers))}
	for id, e := range c.peers {
		f.Peers[strconv.FormatInt(id, 10)] = e
	}
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if err := write(c.path, b); err != nil {
		return err
	}
	c.dirty = false
	return nil
}
