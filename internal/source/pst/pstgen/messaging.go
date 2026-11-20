package pstgen

import (
	"encoding/binary"
	"sync/atomic"
)

// Messaging layer ([MS-PST] 2.4): message store, name-to-ID map, folders
// with hierarchy/contents/associated-contents tables, messages with
// recipient and attachment tables, attachments (by value or embedded
// message).

// File is a whole PST: the store and the folders under its IPM subtree
// ("Top of Personal Folders").
type File struct {
	Crypt     byte
	StoreName string
	Folders   []*Folder
	// NoIPMEntryID leaves PidTagIpmSubTreeEntryId out of the store, so
	// readers must fall back to the root folder.
	NoIPMEntryID bool
	// Extra are additional raw nodes (e.g. corrupt ones).
	Extra []Node
}

// Folder is a normal folder.
type Folder struct {
	Name     string
	Folders  []*Folder
	Messages []*Message
	NID      uint32 // set by Bytes
}

// Message is a message object. Props are its PC properties; the message
// class defaults to IPM.Note.
type Message struct {
	Props       []Prop
	Categories  []string
	Recipients  []Recipient
	Attachments []*Attachment
	NID         uint32 // set by Bytes for top-level messages
	// Corrupt replaces the message node's data with garbage.
	Corrupt bool
}

// Recipient types.
const (
	To  = 1
	Cc  = 2
	Bcc = 3
)

// Recipient is a recipient table row.
type Recipient struct {
	Type                  int32
	Name, Email, AddrType string
	SMTP                  string // PidTagSmtpAddress, optional
}

// Attachment is an attachment object: Data by value, or Embedded.
type Attachment struct {
	Props    []Prop
	Data     []byte
	Embedded *Message
	// Size, when non-zero, is written as PidTagAttachSize instead of
	// len(Data) (a large claimed size without large data).
	Size int32
}

// Property IDs used by the writer.
const (
	PidDisplayName      = 0x3001
	PidMessageClass     = 0x001A
	PidAttachMethod     = 0x3705
	PidAttachData       = 0x3701
	PidAttachSize       = 0x0E20
	PidAttachFilename   = 0x3704
	PidAttachLongName   = 0x3707
	PidRenderingPos     = 0x370B
	PidRecipientType    = 0x0C15
	PidEmailAddress     = 0x3003
	PidAddrType         = 0x3002
	PidSMTPAddress      = 0x39FE
	PidIPMSubtreeEntry  = 0x35E0
	PidRecordKey        = 0x0FF9
	PidContentCount     = 0x3602
	PidContentUnread    = 0x3603
	PidSubfolders       = 0x360A
	PidSubject          = 0x0037
	PidMessageFlags     = 0x0E07
	KeywordsPropID      = 0x8005 // where the writer maps "Keywords"
	otherNamedPropID    = 0x8001
	nidMessageStore     = 0x21
	nidNameToIDMap      = 0x61
	nidRootFolder       = 0x122
	nidIPMSubtree       = 0x8022
	nidSearchRoot       = 0x8042
	nidAttachmentTable  = 0x671
	nidRecipientTable   = 0x692
	nidTypeFolder       = 0x02
	nidTypeMessage      = 0x04
	nidTypeAttachment   = 0x05
	nidTypeHierarchy    = 0x0D
	nidTypeContents     = 0x0E
	nidTypeAssocContent = 0x0F
)

var storeUID = []byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF, 0x01}

// psPublicStrings is PS_PUBLIC_STRINGS {00020329-0000-0000-C000-000000000046}.
var psPublicStrings = []byte{0x29, 0x03, 0x02, 0x00, 0x00, 0x00, 0x00, 0x00, 0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46}

// psetidCommon is PSETID_Common {00062008-0000-0000-C000-000000000046}.
var psetidCommon = []byte{0x08, 0x20, 0x06, 0x00, 0x00, 0x00, 0x00, 0x00, 0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46}

var nextSub atomic.Uint32

func init() { nextSub.Store(0x100) }

// subNID returns a fresh LTP subnode NID (unique in the process, so values
// of different objects of one node never collide).
func subNID() uint32 { return nextSub.Add(1)<<5 | 0x1F }

type fileBuilder struct {
	nodes   []Node
	nextIdx uint32
}

func (b *fileBuilder) nid(typ uint32) uint32 {
	b.nextIdx++
	return b.nextIdx<<5 | typ
}

func (b *fileBuilder) add(nid, parent uint32, o Built) {
	b.nodes = append(b.nodes, Node{NID: nid, Parent: parent, Blocks: o.Blocks, Subnodes: o.Subnodes})
}

var hierCols = []Col{{PidDisplayName, TypeUnicode}, {PidContentCount, TypeInt32}, {PidContentUnread, TypeInt32}, {PidSubfolders, TypeBool}}
var contentsCols = []Col{{PidMessageClass, TypeUnicode}, {PidSubject, TypeUnicode}, {PidMessageFlags, TypeInt32}}

// Bytes serialises the file.
func (f *File) Bytes() []byte {
	b := &fileBuilder{nextIdx: 0x500}
	store := []Prop{Str(PidDisplayName, f.StoreName), Bin(PidRecordKey, storeUID)}
	if !f.NoIPMEntryID {
		eid := append(make([]byte, 4), storeUID...)
		store = append(store, Bin(PidIPMSubtreeEntry, binary.LittleEndian.AppendUint32(eid, nidIPMSubtree)))
	}
	b.add(nidMessageStore, 0, PC(store))
	b.add(nidNameToIDMap, 0, nameMap())

	b.folder(nidRootFolder, nidRootFolder, "", []Row{
		{ID: nidIPMSubtree, Props: []Prop{Str(PidDisplayName, "Top of Personal Folders")}},
		{ID: nidSearchRoot, Props: []Prop{Str(PidDisplayName, "Search Root")}},
	}, nil)
	b.folder(nidSearchRoot, nidRootFolder, "Search Root", nil, nil)
	b.folderTree(nidIPMSubtree, nidRootFolder, "Top of Personal Folders", f.Folders, nil)
	db := &NDB{Crypt: f.Crypt, Nodes: append(b.nodes, f.Extra...)}
	return db.Bytes()
}

func (b *fileBuilder) folderTree(nid, parent uint32, name string, subs []*Folder, msgs []*Message) {
	var rows []Row
	for _, s := range subs {
		s.NID = b.nid(nidTypeFolder)
		rows = append(rows, Row{ID: s.NID, Props: []Prop{Str(PidDisplayName, s.Name), I32(PidContentCount, int32(len(s.Messages))),
			Bool(PidSubfolders, len(s.Folders) > 0)}})
	}
	b.folder(nid, parent, name, rows, msgs)
	for _, s := range subs {
		b.folderTree(s.NID, nid, s.Name, s.Folders, s.Messages)
	}
}

// folder writes a folder PC and its three tables, and its messages.
func (b *fileBuilder) folder(nid, parent uint32, name string, children []Row, msgs []*Message) {
	b.add(nid, parent, PC([]Prop{Str(PidDisplayName, name), I32(PidContentCount, int32(len(msgs))), Bool(PidSubfolders, len(children) > 0)}))
	base := nid &^ 0x1F
	b.add(base|nidTypeHierarchy, 0, TC(hierCols, children))
	var rows []Row
	for _, m := range msgs {
		m.NID = b.nid(nidTypeMessage)
		var props []Prop
		for _, p := range m.Props {
			for _, c := range contentsCols {
				if p.ID == c.ID && p.Type == c.Type {
					props = append(props, p)
				}
			}
		}
		rows = append(rows, Row{ID: m.NID, Props: props})
		if m.Corrupt {
			b.add(m.NID, nid, Built{Blocks: [][]byte{[]byte("this is not a heap-on-node")}})
			continue
		}
		b.add(m.NID, nid, message(m))
	}
	b.add(base|nidTypeContents, 0, TC(contentsCols, rows))
	b.add(base|nidTypeAssocContent, 0, TC(contentsCols, nil))
}

var recipCols = []Col{{PidRecipientType, TypeInt32}, {PidDisplayName, TypeUnicode}, {PidEmailAddress, TypeUnicode},
	{PidAddrType, TypeUnicode}, {PidSMTPAddress, TypeUnicode}}
var attachCols = []Col{{PidAttachSize, TypeInt32}, {PidAttachFilename, TypeUnicode}, {PidAttachMethod, TypeInt32}, {PidRenderingPos, TypeInt32}}

// message builds a message object (PC plus recipient/attachment tables and
// attachment subnodes).
func message(m *Message) Built {
	props := m.Props
	hasClass := false
	for _, p := range props {
		hasClass = hasClass || p.ID == PidMessageClass
	}
	if !hasClass {
		props = append(props, Str(PidMessageClass, "IPM.Note"))
	}
	if len(m.Categories) > 0 {
		props = append(props, MVStr(KeywordsPropID, m.Categories))
	}
	var subs []Subnode
	var rrows []Row
	for i, r := range m.Recipients {
		p := []Prop{I32(PidRecipientType, r.Type), Str(PidDisplayName, r.Name), Str(PidEmailAddress, r.Email), Str(PidAddrType, r.AddrType)}
		if r.SMTP != "" {
			p = append(p, Str(PidSMTPAddress, r.SMTP))
		}
		rrows = append(rrows, Row{ID: uint32(i), Props: p})
	}
	rt := TC(recipCols, rrows)
	subs = append(subs, Subnode{NID: nidRecipientTable, Blocks: rt.Blocks, Subnodes: rt.Subnodes})
	if len(m.Attachments) > 0 {
		var arows []Row
		for i, a := range m.Attachments {
			anid := uint32(0x400+i)<<5 | nidTypeAttachment
			ap := append([]Prop(nil), a.Props...)
			var extra []Subnode
			method := int32(1)
			if a.Embedded != nil {
				method = 5
				emb := message(a.Embedded)
				enid := uint32(0x7000+i)<<5 | nidTypeMessage
				extra = append(extra, Subnode{NID: enid, Blocks: emb.Blocks, Subnodes: emb.Subnodes})
				ap = append(ap, Prop{PidAttachData, TypeObject, binary.LittleEndian.AppendUint32(nil, enid)})
			} else {
				ap = append(ap, Bin(PidAttachData, a.Data))
			}
			size := a.Size
			if size == 0 {
				size = int32(len(a.Data))
			}
			ap = append(ap, I32(PidAttachMethod, method), I32(PidAttachSize, size), I32(PidRenderingPos, -1))
			pc := PC(ap, extra...)
			subs = append(subs, Subnode{NID: anid, Blocks: pc.Blocks, Subnodes: pc.Subnodes})
			row := []Prop{I32(PidAttachSize, size), I32(PidAttachMethod, method), I32(PidRenderingPos, -1)}
			for _, p := range a.Props {
				if p.ID == PidAttachFilename {
					row = append(row, p)
				}
			}
			arows = append(arows, Row{ID: anid, Props: row})
		}
		at := TC(attachCols, arows)
		subs = append(subs, Subnode{NID: nidAttachmentTable, Blocks: at.Blocks, Subnodes: at.Subnodes})
	}
	pc := PC(props)
	pc.Subnodes = append(pc.Subnodes, subs...)
	return pc
}

// nameMap builds the name-to-ID map with "Keywords" (PS_PUBLIC_STRINGS)
// at KeywordsPropID and a numeric PSETID_Common property before it.
func nameMap() Built {
	strs := binary.LittleEndian.AppendUint32(nil, uint32(len(UTF16("Keywords"))))
	strs = append(strs, UTF16("Keywords")...)
	var entries []byte
	// Numeric LID 0x8530 in GUID stream entry 0 (wGuid 3).
	entries = binary.LittleEndian.AppendUint32(entries, 0x8530)
	entries = binary.LittleEndian.AppendUint16(entries, 3<<1)
	entries = binary.LittleEndian.AppendUint16(entries, otherNamedPropID-0x8000)
	// String name at offset 0, PS_PUBLIC_STRINGS (wGuid 2).
	entries = binary.LittleEndian.AppendUint32(entries, 0)
	entries = binary.LittleEndian.AppendUint16(entries, 2<<1|1)
	entries = binary.LittleEndian.AppendUint16(entries, KeywordsPropID-0x8000)
	return PC([]Prop{I32(0x0001, 251), Bin(0x0002, psetidCommon), Bin(0x0003, entries), Bin(0x0004, strs)})
}
