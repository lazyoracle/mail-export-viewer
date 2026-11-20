package server

import (
	"time"

	"github.com/lazyoracle/mail-export-viewer/internal/index"
	"github.com/lazyoracle/mail-export-viewer/internal/mail"
	"github.com/lazyoracle/mail-export-viewer/internal/source"
)

// JSON shapes of the API (documented in docs/api.md). Every string field
// is plain text for the UI to insert as text, except those whose name ends
// in "Html", which hold sanitised, safe-to-insert HTML.

type addrJSON struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

type tagJSON struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

type itemJSON struct {
	ID            int64     `json:"id"`
	ThreadID      int64     `json:"threadId"`
	ThreadCount   int       `json:"threadCount"`
	SourceID      int64     `json:"sourceId"`
	SourceKind    string    `json:"sourceKind"`
	FolderID      int64     `json:"folderId"`
	Folder        string    `json:"folder"`
	Subject       string    `json:"subject"`
	From          addrJSON  `json:"from"`
	ToText        string    `json:"toText"`
	Date          *string   `json:"date"`
	Flags         []string  `json:"flags"`
	HasAttachment bool      `json:"hasAttachment"`
	Labels        []string  `json:"labels"`
	Categories    []string  `json:"categories"`
	Tags          []tagJSON `json:"tags"`
	Snippet       string    `json:"snippet"`
	HighlightHTML string    `json:"highlightHtml,omitempty"`
}

type listJSON struct {
	Items      []itemJSON `json:"items"`
	Total      int64      `json:"total"`
	NextCursor string     `json:"nextCursor"`
}

type propJSON struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type sourceRefJSON struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

type attachmentJSON struct {
	Index       int    `json:"index"`
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	ContentID   string `json:"contentId"`
	Size        int64  `json:"size"`
	Inline      bool   `json:"inline"`
	URL         string `json:"url"`
}

type detailJSON struct {
	ID                   int64            `json:"id"`
	ThreadID             int64            `json:"threadId"`
	Source               sourceRefJSON    `json:"source"`
	FolderID             int64            `json:"folderId"`
	Folder               string           `json:"folder"`
	MessageID            string           `json:"messageId"`
	InReplyTo            string           `json:"inReplyTo"`
	References           []string         `json:"references"`
	Subject              string           `json:"subject"`
	From                 addrJSON         `json:"from"`
	To                   []addrJSON       `json:"to"`
	Cc                   []addrJSON       `json:"cc"`
	Bcc                  []addrJSON       `json:"bcc"`
	Date                 *string          `json:"date"`
	Flags                []string         `json:"flags"`
	Labels               []string         `json:"labels"`
	Categories           []string         `json:"categories"`
	Tags                 []tagJSON        `json:"tags"`
	Props                []propJSON       `json:"props"`
	Headers              [][2]string      `json:"headers"`
	Text                 string           `json:"text"`
	TextTruncated        bool             `json:"textTruncated"`
	BodyHTML             string           `json:"bodyHtml"`
	HTMLTruncated        bool             `json:"htmlTruncated"`
	RemoteContentBlocked bool             `json:"remoteContentBlocked"`
	Attachments          []attachmentJSON `json:"attachments"`
	RawURL               string           `json:"rawUrl"`
	ParseError           bool             `json:"parseError"`
}

type statusJSON struct {
	Version        string         `json:"version"`
	IncludeDeleted bool           `json:"includeDeleted"`
	Indexing       indexingJSON   `json:"indexing"`
	Sources        []statusSource `json:"sources"`
}

type indexingJSON struct {
	Running  bool   `json:"running"`
	Finished bool   `json:"finished"`
	Messages int64  `json:"messages"`
	Warnings int64  `json:"warnings"`
	Error    string `json:"error"`
}

type statusSource struct {
	ID           int64    `json:"id"`
	Name         string   `json:"name"`
	Kind         string   `json:"kind"`
	State        string   `json:"state"`
	Unit         string   `json:"unit"`
	Done         int64    `json:"done"`
	Total        int64    `json:"total"`
	Messages     int64    `json:"messages"`
	Count        int64    `json:"count"`
	Unread       int64    `json:"unread"`
	Complete     bool     `json:"complete"`
	WarningCount int64    `json:"warningCount"`
	Warnings     []string `json:"warnings"`
	Error        string   `json:"error"`
}

type facetsJSON struct {
	Sources    []sourceJSON `json:"sources"`
	Folders    []folderJSON `json:"folders"`
	Labels     []termJSON   `json:"labels"`
	Categories []termJSON   `json:"categories"`
	Tags       []termJSON   `json:"tags"`
	Flags      []flagJSON   `json:"flags"`
}

type sourceJSON struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Count  int64  `json:"count"`
	Unread int64  `json:"unread"`
}

type folderJSON struct {
	Name     string          `json:"name"`
	Path     string          `json:"path"`
	Count    int64           `json:"count"`
	Unread   int64           `json:"unread"`
	Sources  []folderRefJSON `json:"sources"`
	Children []folderJSON    `json:"children"`
}

type folderRefJSON struct {
	FolderID   int64  `json:"folderId"`
	SourceID   int64  `json:"sourceId"`
	SourceKind string `json:"sourceKind"`
	Source     string `json:"source"`
	Count      int64  `json:"count"`
	Unread     int64  `json:"unread"`
}

type termJSON struct {
	Name   string `json:"name"`
	Color  string `json:"color,omitempty"`
	Count  int64  `json:"count"`
	Unread int64  `json:"unread"`
}

type flagJSON struct {
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

func jsonDate(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func flagNames(f source.Flags) []string { return nonNil(f.Names()) }

func tagsJSON(ts []index.Tag) []tagJSON {
	out := make([]tagJSON, 0, len(ts))
	for _, t := range ts {
		out = append(out, tagJSON{t.Name, t.Color})
	}
	return out
}

func toItem(r index.Row) itemJSON {
	return itemJSON{
		ID: r.ID, ThreadID: r.ThreadID, SourceID: r.SourceID, SourceKind: r.SourceKind, FolderID: r.FolderID,
		Folder: r.Folder, Subject: r.Subject, From: addrJSON{r.FromName, r.FromEmail}, ToText: r.To,
		Date: jsonDate(r.Date), Flags: flagNames(r.Flags), HasAttachment: r.HasAttachment,
		Labels: nonNil(r.Labels), Categories: nonNil(r.Categories), Tags: tagsJSON(r.Tags), Snippet: r.Snippet,
	}
}

func toAddrs(as []mail.Address) []addrJSON {
	out := make([]addrJSON, 0, len(as))
	for _, a := range as {
		out = append(out, addrJSON{a.Name, a.Email})
	}
	return out
}
