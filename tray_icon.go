package main

import (
	"bytes"
	"encoding/binary"
	"image/color"
	"math"
)

// usageLevel là mức màu của icon theo % phiên 5h.
type usageLevel int

const (
	levelUnknown usageLevel = iota
	levelGreen
	levelYellow
	levelRed
)

// levelFor: xanh < 50%, vàng 50–80%, đỏ > 80%.
func levelFor(percent float64, ok bool) usageLevel {
	switch {
	case !ok:
		return levelUnknown
	case percent < 50:
		return levelGreen
	case percent <= 80:
		return levelYellow
	}
	return levelRed
}

var levelIcons = map[usageLevel][]byte{
	levelGreen:  buildCircleICO(color.NRGBA{R: 0x2E, G: 0xA0, B: 0x43, A: 0xFF}),
	levelYellow: buildCircleICO(color.NRGBA{R: 0xE3, G: 0xB3, B: 0x41, A: 0xFF}),
	levelRed:    buildCircleICO(color.NRGBA{R: 0xF8, G: 0x51, B: 0x49, A: 0xFF}),
}

// iconForPercent trả về icon đổi màu; chưa có số thì dùng icon gốc của app.
func iconForPercent(percent float64, ok bool) []byte {
	if ic, found := levelIcons[levelFor(percent, ok)]; found {
		return ic
	}
	return iconData
}

// buildCircleICO dựng file .ico 32x32 (bitmap 32-bit, không dùng PNG nén để Windows nào cũng đọc được)
// gồm hình tròn đặc màu c, viền khử răng cưa.
func buildCircleICO(c color.NRGBA) []byte {
	const size = 32
	const maskRow = (size + 31) / 32 * 4
	imageSize := 40 + size*size*4 + size*maskRow

	le := binary.LittleEndian
	b := make([]byte, 0, 22+imageSize)
	b = le.AppendUint16(b, 0) // ICONDIR: reserved, type=1 (icon), count=1
	b = le.AppendUint16(b, 1)
	b = le.AppendUint16(b, 1)
	b = append(b, size, size, 0, 0)
	b = le.AppendUint16(b, 1)  // planes
	b = le.AppendUint16(b, 32) // bit count
	b = le.AppendUint32(b, uint32(imageSize))
	b = le.AppendUint32(b, 22) // offset của ảnh = 6 + 16
	b = le.AppendUint32(b, 40) // BITMAPINFOHEADER
	b = le.AppendUint32(b, size)
	b = le.AppendUint32(b, size*2) // chiều cao gồm cả AND mask
	b = le.AppendUint16(b, 1)
	b = le.AppendUint16(b, 32)
	b = le.AppendUint32(b, 0)
	b = le.AppendUint32(b, uint32(imageSize-40))
	b = append(b, make([]byte, 16)...) // pixel/m và số màu: 0
	for y := size - 1; y >= 0; y-- {   // DIB lưu từ dưới lên
		for x := 0; x < size; x++ {
			d := math.Hypot(float64(x)-15.5, float64(y)-15.5)
			a := uint8(255 * math.Max(0, math.Min(1, 14.5-d)))
			b = append(b, c.B, c.G, c.R, a)
		}
	}
	return append(b, bytes.Repeat([]byte{0}, size*maskRow)...) // AND mask toàn 0: alpha quyết định độ trong suốt
}
