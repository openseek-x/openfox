package artifacts

import "bytes"

// PDFEnvelope identifies a bounded PDF payload, not its safety or renderability.
// The preview parser still treats every object, action and URL as untrusted.
func PDFEnvelope(data []byte) bool {
	if len(data) < 16 || int64(len(data)) > MaxArtifactBytes || !bytes.HasPrefix(data, []byte("%PDF-")) {
		return false
	}
	if data[5] < '1' || data[5] > '2' || data[6] != '.' || data[7] < '0' || data[7] > '9' {
		return false
	}
	tail := data[max(0, len(data)-1024):]
	return bytes.Contains(tail, []byte("startxref")) && bytes.Contains(tail, []byte("%%EOF"))
}
