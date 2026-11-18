package artifactreview

import (
	"encoding/json"
	"openfox/internal/office"
)

// Only the daemon-center file transfer uses this bound. The model receives
// paths and metadata, never the base64 transport envelope.
const MaxOfficeTransferBytes = 16 << 20

type OfficeReplacement struct {
	SHA256  string `json:"sha256"`
	Summary string `json:"summary"`
	Content []byte `json:"content_base64"`
}

func (r OfficeReplacement) Hash() string {
	raw, _ := json.Marshal(struct{ Kind, SHA256, Summary, ResultSHA256 string }{"office-file", r.SHA256, r.Summary, Digest(string(r.Content))})
	return Digest(string(raw))
}

// ValidateOfficeReplacement accepts a new Office artifact, not a coordinate
// patch. Structure and original feedback are checked; semantic changes must be
// reviewed in the new preview. The original stored object is never overwritten.
func ValidateOfficeReplacement(source, mime string, comments []Snapshot, r OfficeReplacement) error {
	if !office.MIME(mime) || len(r.Content) == 0 || len(r.Content) > office.MaxBytes || len(r.Summary) > 2000 || !ValidText(r.Summary) || r.SHA256 != Digest(source) || Digest(string(r.Content)) == r.SHA256 || len(comments) == 0 {
		return ErrInvalid
	}
	if _, err := office.Read(r.Content, mime); err != nil {
		return ErrInvalid
	}
	for _, c := range comments {
		if err := ValidateAnchor(source, mime, c.Anchor); err != nil {
			return err
		}
	}
	return nil
}
