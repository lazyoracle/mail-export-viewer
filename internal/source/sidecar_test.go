package source

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fakeSidecar answers for one message id.
type fakeSidecar struct{ path string }

func (s fakeSidecar) Lookup(msgID string, offset int64, statusKey string) (SidecarMeta, bool) {
	if msgID != "m1@example.com" && offset != 42 {
		return SidecarMeta{}, false
	}
	return SidecarMeta{Flags: Starred, Tags: []string{"Important"}, Props: []Prop{{"junkscore", "0"}}}, true
}
func (s fakeSidecar) Inputs() []string { return []string{s.path + ".msf"} }
func (s fakeSidecar) TagInfo() map[string]TagStyle {
	return map[string]TagStyle{"$label1": {Name: "Important", Color: "#FF0000"}}
}

func useSidecarProviders(t *testing.T, ps ...func(string) Sidecar) {
	t.Helper()
	sideMu.Lock()
	saved := sideProviders
	sideProviders = nil
	sideMu.Unlock()
	t.Cleanup(func() {
		sideMu.Lock()
		sideProviders = saved
		sideMu.Unlock()
	})
	for _, p := range ps {
		RegisterSidecarProvider(p)
	}
}

func TestSidecarFor(t *testing.T) {
	useSidecarProviders(t)
	if SidecarFor("/x/Inbox") != nil {
		t.Fatal("no providers: want nil")
	}
	none := func(string) Sidecar { return nil }
	msf := func(p string) Sidecar {
		if strings.HasSuffix(p, "Inbox") {
			return fakeSidecar{p}
		}
		return nil
	}
	useSidecarProviders(t, none, msf)
	inbox := filepath.Join("/x", "Inbox")
	sc := SidecarFor(inbox)
	if sc == nil {
		t.Fatal("SidecarFor(Inbox) = nil")
	}
	if SidecarFor("/x/Sent") != nil {
		t.Error("SidecarFor(Sent) != nil")
	}
	m, ok := sc.Lookup("m1@example.com", 0, "")
	if !ok || m.Flags != Starred || !reflect.DeepEqual(m.Tags, []string{"Important"}) {
		t.Errorf("Lookup = %+v %v", m, ok)
	}
	if _, ok := sc.Lookup("other@example.com", 7, ""); ok {
		t.Error("Lookup(other) ok")
	}
	if !reflect.DeepEqual(sc.Inputs(), []string{inbox + ".msf"}) || sc.TagInfo()["$label1"].Name != "Important" {
		t.Errorf("Inputs %q TagInfo %v", sc.Inputs(), sc.TagInfo())
	}
	defer func() {
		if recover() == nil {
			t.Error("RegisterSidecarProvider(nil) did not panic")
		}
	}()
	RegisterSidecarProvider(nil)
}
