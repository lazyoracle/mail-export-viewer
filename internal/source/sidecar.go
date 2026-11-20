package source

import "sync"

// SidecarMeta is per-message metadata a sidecar file (a Thunderbird .msf
// summary) supplies for a message of an mbox.
type SidecarMeta struct {
	// Flags replace the header-derived flags (the sidecar is the more
	// recent truth: Thunderbird updates it, not the mbox), except
	// HasAttachment, which Resolve derives from the message.
	Flags        Flags
	Tags, Labels []string // display names, appended to Entry.Tags/Labels (duplicates dropped)
	Props        []Prop   // appended to Entry.Props
}

// TagStyle is how a tag is shown: display name and colour ("#RRGGBB", or ""
// for the default).
type TagStyle struct{ Name, Color string }

// Sidecar is the metadata of one mbox file kept in a separate file. It is
// the seam between the mbox source (consumer) and the msf package
// (provider), which never import each other.
type Sidecar interface {
	// Lookup finds the metadata of the message with Message-ID msgID (no
	// angle brackets, may be ""), starting at byte offset in the mbox (its
	// From_ line), whose X-Mozilla-Status/message key is statusKey (may be
	// ""). Providers match by whatever keys they have; ok is false when the
	// message is unknown.
	Lookup(msgID string, offset int64, statusKey string) (meta SidecarMeta, ok bool)
	// Inputs are the files the sidecar read (added to the mbox source's
	// Inputs, so the cache notices changes).
	Inputs() []string
	// TagInfo maps tag keys ("$label1", "work", as in X-Mozilla-Keys) to
	// display name and colour; may be nil.
	TagInfo() map[string]TagStyle
}

var (
	sideMu        sync.RWMutex
	sideProviders []func(mboxPath string) Sidecar
)

// RegisterSidecarProvider adds a provider, called by SidecarFor with an
// mbox path; it returns nil when it has no sidecar for that mbox. Format
// packages call it from init. It panics on nil.
func RegisterSidecarProvider(p func(mboxPath string) Sidecar) {
	if p == nil {
		panic("source: RegisterSidecarProvider(nil)")
	}
	sideMu.Lock()
	defer sideMu.Unlock()
	sideProviders = append(sideProviders, p)
}

// SidecarFor returns the first non-nil sidecar a registered provider offers
// for the mbox at mboxPath (absolute), or nil.
func SidecarFor(mboxPath string) Sidecar {
	sideMu.RLock()
	ps := append([]func(string) Sidecar(nil), sideProviders...)
	sideMu.RUnlock()
	for _, p := range ps {
		if sc := p(mboxPath); sc != nil {
			return sc
		}
	}
	return nil
}
