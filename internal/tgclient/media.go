package tgclient

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"

	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/output"
)

// rawMessage fetches one message with its entities.
func (s *Session) rawMessage(ctx context.Context, p *Peer, id int) (*tg.Message, error) {
	in := []tg.InputMessageClass{&tg.InputMessageID{ID: id}}
	var r tg.MessagesMessagesClass
	var err error
	if p.Kind == KindChannel {
		r, err = s.api.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{
			Channel: &tg.InputChannel{ChannelID: p.RawID, AccessHash: p.AccessHash}, ID: in,
		})
	} else {
		r, err = s.api.MessagesGetMessages(ctx, in)
	}
	if err != nil {
		return nil, err
	}
	pr := unpackMessages(r)
	s.absorb(pr.users, pr.chats)
	for _, mc := range pr.messages {
		if m, ok := mc.(*tg.Message); ok && m.ID == id && PeerMarkedID(m.PeerID) == p.ID {
			return m, nil
		}
	}
	return nil, output.NotFound("message %d not found in chat %d", id, p.ID)
}

// fileLocation returns the download location of a message's photo or document.
func fileLocation(m *tg.Message) (tg.InputFileLocationClass, error) {
	switch md := m.Media.(type) {
	case *tg.MessageMediaPhoto:
		if p, ok := md.Photo.(*tg.Photo); ok {
			size, _ := largestPhotoSize(p)
			if size != "" {
				return &tg.InputPhotoFileLocation{ID: p.ID, AccessHash: p.AccessHash, FileReference: p.FileReference, ThumbSize: size}, nil
			}
		}
	case *tg.MessageMediaDocument:
		if d, ok := md.Document.(*tg.Document); ok {
			return &tg.InputDocumentFileLocation{ID: d.ID, AccessHash: d.AccessHash, FileReference: d.FileReference}, nil
		}
	}
	return nil, output.NotFound("message %d has no downloadable media", m.ID)
}

func (s *Session) DownloadMedia(ctx context.Context, p *Peer, msgID int, dir string) (*Download, error) {
	m, err := s.rawMessage(ctx, p, msgID)
	if err != nil {
		return nil, err
	}
	info := MediaInfo(m.Media)
	if info == nil {
		return nil, output.NotFound("message %d has no media", msgID)
	}
	if _, err := fileLocation(m); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path, f, err := createUnique(dir, MediaFilename(info, unix(m.Date)))
	if err != nil {
		return nil, err
	}
	err = s.download(ctx, m, f)
	if isRPC(err, "FILE_REFERENCE_EXPIRED", "FILE_REFERENCE_INVALID") {
		// File references expire; refetch the message once.
		if m, err = s.rawMessage(ctx, p, msgID); err == nil {
			if err = f.Truncate(0); err == nil {
				_, _ = f.Seek(0, 0)
				err = s.download(ctx, m, f)
			}
		}
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(path)
		return nil, err
	}
	abs, _ := filepath.Abs(path)
	return &Download{Path: abs, Media: info}, nil
}

func (s *Session) download(ctx context.Context, m *tg.Message, f *os.File) error {
	loc, err := fileLocation(m)
	if err != nil {
		return err
	}
	_, err = downloader.NewDownloader().Download(s.api, loc).Stream(ctx, f)
	return err
}

var mimeExt = map[string]string{
	"image/jpeg":              ".jpg",
	"image/png":               ".png",
	"image/webp":              ".webp",
	"image/gif":               ".gif",
	"video/mp4":               ".mp4",
	"video/webm":              ".webm",
	"video/quicktime":         ".mov",
	"audio/ogg":               ".oga",
	"audio/mpeg":              ".mp3",
	"audio/mp4":               ".m4a",
	"application/pdf":         ".pdf",
	"application/x-tgsticker": ".tgs",
	"application/zip":         ".zip",
}

func extFor(mimeType string) string {
	if e, ok := mimeExt[mimeType]; ok {
		return e
	}
	if exts, _ := mime.ExtensionsByType(mimeType); len(exts) > 0 {
		return exts[0]
	}
	return ""
}

// MediaFilename is the attachment's own (sanitised) name, else Telethon-style kind_date.ext.
func MediaFilename(info *Media, date time.Time) string {
	if n := SanitizeFilename(info.Name); n != "" {
		return n
	}
	return fmt.Sprintf("%s_%s%s", info.Type, date.UTC().Format("2006-01-02_15-04-05"), extFor(info.Mime))
}

// SanitizeFilename keeps only a safe base name: no directories, no leading dots, no control chars.
func SanitizeFilename(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = filepath.Base(name)
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '/' {
			return -1
		}
		return r
	}, name)
	name = strings.TrimLeft(strings.TrimSpace(name), ".")
	if len(name) > 200 {
		ext := filepath.Ext(name)
		if len(ext) > 20 {
			ext = ""
		}
		name = truncateBytes(name[:len(name)-len(ext)], 200-len(ext)) + ext
	}
	return name
}

func truncateBytes(s string, n int) string {
	for len(s) > n {
		r := []rune(s)
		s = string(r[:len(r)-1])
	}
	return s
}

// createUnique creates dir/name exclusively, adding " (1)", " (2)"… on collisions.
func createUnique(dir, name string) (string, *os.File, error) {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 0; i < 1000; i++ {
		n := name
		if i > 0 {
			n = fmt.Sprintf("%s (%d)%s", stem, i, ext)
		}
		path := filepath.Join(dir, n)
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			return path, f, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return "", nil, err
		}
	}
	return "", nil, fmt.Errorf("too many files named %s in %s", name, dir)
}

const maxPhotoBytes = 10 << 20

func uploadMedia(ctx context.Context, api *tg.Client, path string) (tg.InputMediaClass, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, output.Usage("cannot read %s: %v", path, err)
	}
	if !st.Mode().IsRegular() {
		return nil, output.Usage("%s is not a regular file", path)
	}
	f, err := uploader.NewUploader(api).FromPath(ctx, path)
	if err != nil {
		return nil, err
	}
	ext := strings.ToLower(filepath.Ext(path))
	if (ext == ".jpg" || ext == ".jpeg" || ext == ".png") && st.Size() <= maxPhotoBytes {
		return &tg.InputMediaUploadedPhoto{File: f}, nil
	}
	mt := mime.TypeByExtension(ext)
	if i := strings.IndexByte(mt, ';'); i >= 0 {
		mt = mt[:i]
	}
	if mt == "" {
		mt = "application/octet-stream"
	}
	return &tg.InputMediaUploadedDocument{
		File: f, MimeType: mt,
		Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: filepath.Base(path)}},
	}, nil
}
