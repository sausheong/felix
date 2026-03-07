package main

import (
	"bytes"
	"encoding/binary"
	"image/png"
)

// trayIcon converts the embedded PNG to ICO format for Windows.
// The ICO format supports raw PNG payloads (since Windows Vista).
func trayIcon(pngData []byte) []byte {
	// Decode PNG to get dimensions
	img, err := png.Decode(bytes.NewReader(pngData))
	if err != nil {
		return pngData // fallback to raw PNG
	}
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()

	// ICO dimensions use 0 to represent 256
	iconW, iconH := byte(w), byte(h)
	if w >= 256 {
		iconW = 0
	}
	if h >= 256 {
		iconH = 0
	}

	var buf bytes.Buffer

	// ICO header (6 bytes)
	binary.Write(&buf, binary.LittleEndian, uint16(0))     // reserved
	binary.Write(&buf, binary.LittleEndian, uint16(1))     // type: 1 = ICO
	binary.Write(&buf, binary.LittleEndian, uint16(1))     // image count

	// ICO directory entry (16 bytes)
	buf.WriteByte(iconW)                                           // width
	buf.WriteByte(iconH)                                           // height
	buf.WriteByte(0)                                               // color palette count
	buf.WriteByte(0)                                               // reserved
	binary.Write(&buf, binary.LittleEndian, uint16(1))             // color planes
	binary.Write(&buf, binary.LittleEndian, uint16(32))            // bits per pixel
	binary.Write(&buf, binary.LittleEndian, uint32(len(pngData)))  // image data size
	binary.Write(&buf, binary.LittleEndian, uint32(6+16))          // offset to image data (header + 1 entry)

	// PNG payload
	buf.Write(pngData)

	return buf.Bytes()
}
