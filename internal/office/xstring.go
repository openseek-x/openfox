package office

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"
)

// Spreadsheet ST_Xstring has a second escaping layer beyond XML entities.
// Decode once: _x005F_x000D_ represents the literal text _x000D_.
var xescape = regexp.MustCompile(`_x[0-9a-fA-F]{4}_`)

func decodeXString(s string) string {
	matches := xescape.FindAllStringIndex(s, -1)
	if len(matches) == 0 {
		return s
	}
	var code []uint16
	pos := 0
	for _, m := range matches {
		code = append(code, utf16.Encode([]rune(s[pos:m[0]]))...)
		v, _ := strconv.ParseUint(s[m[0]+2:m[1]-1], 16, 16)
		code = append(code, uint16(v))
		pos = m[1]
	}
	code = append(code, utf16.Encode([]rune(s[pos:]))...)
	return string(utf16.Decode(code))
}
func encodeXString(s string) string {
	s = xescape.ReplaceAllStringFunc(s, func(match string) string { return "_x005F_" + match[1:] })
	return strings.ReplaceAll(s, "\r", "_x000D_")
}
