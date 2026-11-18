package integrations

import "strings"

const wecomMarkdownLimit = 4000

func buildWeComNotification(kind, heading, summary, link string) string {
	var content strings.Builder
	content.WriteString("### " + cardTitle(kind) + "\n\n")
	if heading != "" {
		content.WriteString("**" + dingTalkMDEscaper.Replace(heading) + "**\n\n")
	}
	content.WriteString(dingTalkMDEscaper.Replace(cardSummary(summary)))
	if link != "" {
		content.WriteString("\n\n[" + notifyButtonText + "](" + link + ")")
	}
	return limitRunes(content.String(), wecomMarkdownLimit)
}

func buildWeComReply(body, link string) string {
	content := strings.TrimSpace(body)
	if link != "" {
		content += "\n\n[" + replyLinkText + "](" + link + ")"
	}
	return limitRunes(content, wecomMarkdownLimit)
}
