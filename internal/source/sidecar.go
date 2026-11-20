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
	// From_ line; negative when unknown). Providers match by whatever keys
	// they have; ok is false when the message is unknown.
	Lookup(msgID string, offset int64) (meta SidecarMeta, ok bool)
	// Inputs are the files the sidecar read (added to the mbox source's
	// Inputs, so the cache notices changes).
	Inputs() []string
	// TagInfo maps tag keys ("$label1", "work", as in X-Mozilla-Keys) to
	// display name and colour; may be nil.
	TagInfo() map[string]TagStyle
}

var (
	sideMu        sync.RWMutex
	sideProviders []func(mboxPath string) (Sidecar, error)
)

// RegisterSidecarProvider adds a provider, called by SidecarFor with an
// mbox path. It returns (nil, nil) when it has no sidecar for that mbox,
// (nil, err) when the sidecar is unusable (corrupt, oversized, unreadable),
// and (sc, err) with both non-nil when it is usable but degraded (err is a
// warning). Format
// packages call it from init. It panics on nil.
func RegisterSidecarProvider(p func(mboxPath string) (Sidecar, error)) {
	if p == nil {
		panic("source: RegisterSidecarProvider(nil)")
	}
	sideMu.Lock()
	defer sideMu.Unlock()
	sideProviders = append(sideProviders, p)
}

// SidecarFor returns the result of the first registered provider that has
// something to say for the mbox at mboxPath (absolute): a sidecar, an error,
// or both (usable but degraded; the caller reports err as a warning and
// still uses the sidecar). (nil, nil) means there is no sidecar.
func SidecarFor(mboxPath string) (Sidecar, error) {
	sideMu.RLock()
	ps := append([]func(string) (Sidecar, error)(nil), sideProviders...)
	sideMu.RUnlock()
	for _, p := range ps {
		if sc, err := p(mboxPath); sc != nil || err != nil {
			return sc, err
		}
	}
	return nil, nil
}
