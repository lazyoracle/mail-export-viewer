package source

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fakeSidecar answers for one message id.
type fakeSidecar struct{ path string }

func (s fakeSidecar) Lookup(msgID string, offset int64) (SidecarMeta, bool) {
	if msgID != "m1@example.com" && offset != 42 {
		return SidecarMeta{}, false
	}
	return SidecarMeta{Flags: Starred, Tags: []string{"Important"}, Props: []Prop{{"junkscore", "0"}}}, true
}
func (s fakeSidecar) Inputs() []string { return []string{s.path + ".msf"} }
func (s fakeSidecar) TagInfo() map[string]TagStyle {
	return map[string]TagStyle{"$label1": {Name: "Important", Color: "#FF0000"}}
}

func useSidecarProviders(t *testing.T, ps ...func(string) (Sidecar, error)) {
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
	if sc, err := SidecarFor("/x/Inbox"); sc != nil || err != nil {
		t.Fatal("no providers: want nil")
	}
	none := func(string) (Sidecar, error) { return nil, nil }
	msf := func(p string) (Sidecar, error) {
		if strings.HasSuffix(p, "Inbox") {
			return fakeSidecar{p}, nil
		}
		if strings.HasSuffix(p, "Bad") {
			return nil, errors.New("corrupt")
		}
		if strings.HasSuffix(p, "Meh") {
			return fakeSidecar{p}, errors.New("degraded")
		}
		return nil, nil
	}
	useSidecarProviders(t, none, msf)
	inbox := filepath.Join("/x", "Inbox")
	sc, err := SidecarFor(inbox)
	if sc == nil || err != nil {
		t.Fatalf("SidecarFor(Inbox) = %v, %v", sc, err)
	}
	if sc, err := SidecarFor("/x/Sent"); sc != nil || err != nil {
		t.Errorf("SidecarFor(Sent) = %v, %v", sc, err)
	}
	if sc, err := SidecarFor("/x/Bad"); sc != nil || err == nil {
		t.Errorf("SidecarFor(Bad) = %v, %v; want nil sidecar and an error", sc, err)
	}
	if sc, err := SidecarFor("/x/Meh"); sc == nil || err == nil {
		t.Errorf("SidecarFor(Meh) = %v, %v; want usable sidecar and a warning", sc, err)
	}
	m, ok := sc.Lookup("m1@example.com", 0)
	if !ok || m.Flags != Starred || !reflect.DeepEqual(m.Tags, []string{"Important"}) {
		t.Errorf("Lookup = %+v %v", m, ok)
	}
	if _, ok := sc.Lookup("other@example.com", 7); ok {
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
