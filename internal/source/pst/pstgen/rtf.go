package pstgen

import "encoding/binary"

// CompressRTF encodes src as [MS-OXRTFCP] compressed RTF ("LZFu") using
// literals only (valid, if not small), ending with the end-of-stream
// reference.
func CompressRTF(src []byte) []byte {
	const prebuf = 207
	var out []byte
	pos, i := prebuf, 0
	for {
		ctrl := len(out)
		out = append(out, 0)
		for bit := 0; bit < 8; bit++ {
			if i == len(src) {
				out[ctrl] |= 1 << bit
				ref := uint16(pos%4096) << 4
				out = append(out, byte(ref>>8), byte(ref))
				h := binary.LittleEndian.AppendUint32(nil, uint32(len(out)+12))
				h = binary.LittleEndian.AppendUint32(h, uint32(len(src)))
				h = append(h, "LZFu"...)
				h = binary.LittleEndian.AppendUint32(h, CRC(out))
				return append(h, out...)
			}
			out = append(out, src[i])
			i++
			pos++
		}
	}
}
