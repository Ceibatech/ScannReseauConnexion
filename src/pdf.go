package main

// Assemblage d'un PDF à partir des pages JPEG du scanner, sans réencodage : chaque JPEG est
// inclus tel quel (filtre DCTDecode), donc aucune perte de qualité pour l'OCR et les QR.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image/color"
	"image/jpeg"
)

// jpegDPI lit la densité de l'en-tête JFIF ; 0 si elle est absente ou sans unité.
func jpegDPI(data []byte) (int, int) {
	if len(data) < 18 || data[0] != 0xFF || data[1] != 0xD8 || data[2] != 0xFF || data[3] != 0xE0 ||
		string(data[6:11]) != "JFIF\x00" {
		return 0, 0
	}
	x, y := int(binary.BigEndian.Uint16(data[14:16])), int(binary.BigEndian.Uint16(data[16:18]))
	switch data[13] {
	case 1: // points par pouce
		return x, y
	case 2: // points par centimètre
		return x * 254 / 100, y * 254 / 100
	}
	return 0, 0
}

// buildPDF assemble les pages dans l'ordre. dpi sert quand le JPEG n'indique pas sa résolution.
func buildPDF(pages [][]byte, dpi int) ([]byte, error) {
	if len(pages) == 0 {
		return nil, errors.New("aucune page")
	}
	var out bytes.Buffer
	var offsets []int
	object := func() int {
		offsets = append(offsets, out.Len())
		n := len(offsets)
		fmt.Fprintf(&out, "%d 0 obj\n", n)
		return n
	}
	out.WriteString("%PDF-1.4\n%\xE2\xE3\xCF\xD3\n")
	object()
	out.WriteString("<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
	object()
	kids := ""
	for i := range pages {
		kids += fmt.Sprintf("%d 0 R ", 3+3*i)
	}
	fmt.Fprintf(&out, "<< /Type /Pages /Count %d /Kids [ %s] >>\nendobj\n", len(pages), kids)
	for i, page := range pages {
		info, err := jpeg.DecodeConfig(bytes.NewReader(page))
		if err != nil {
			return nil, fmt.Errorf("page %d : JPEG illisible : %w", i+1, err)
		}
		space := "/DeviceRGB"
		switch info.ColorModel {
		case color.GrayModel:
			space = "/DeviceGray"
		case color.CMYKModel:
			return nil, fmt.Errorf("page %d : JPEG CMJN non pris en charge", i+1)
		}
		xdpi, ydpi := jpegDPI(page)
		if xdpi < 50 || ydpi < 50 {
			xdpi, ydpi = dpi, dpi
		}
		w := float64(info.Width) * 72 / float64(xdpi)
		h := float64(info.Height) * 72 / float64(ydpi)
		pageID, contentID, imageID := 3+3*i, 4+3*i, 5+3*i
		content := fmt.Sprintf("q %.2f 0 0 %.2f 0 0 cm /Im0 Do Q", w, h)
		if object() != pageID {
			return nil, errors.New("numérotation PDF incohérente")
		}
		fmt.Fprintf(&out, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %.2f %.2f] /Resources << /XObject << /Im0 %d 0 R >> >> /Contents %d 0 R >>\nendobj\n",
			w, h, imageID, contentID)
		object()
		fmt.Fprintf(&out, "<< /Length %d >>\nstream\n%s\nendstream\nendobj\n", len(content), content)
		object()
		fmt.Fprintf(&out, "<< /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace %s /BitsPerComponent 8 /Filter /DCTDecode /Length %d >>\nstream\n",
			info.Width, info.Height, space, len(page))
		out.Write(page)
		out.WriteString("\nendstream\nendobj\n")
	}
	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(offsets)+1)
	for _, offset := range offsets {
		fmt.Fprintf(&out, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets)+1, xref)
	return out.Bytes(), nil
}
