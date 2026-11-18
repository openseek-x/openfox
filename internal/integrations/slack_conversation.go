package integrations

import "openfox/internal/integrations/slack"

func slackOrdinaryConversation(c slack.Conversation, team, user string, dm bool) bool {
	yes := func(b *bool) bool { return b != nil && *b }
	no := func(b *bool) bool { return b != nil && !*b }
	if yes(c.IsMPIM) || yes(c.IsShared) || yes(c.IsExtShared) || yes(c.IsOrgShared) || yes(c.IsPendingExtShared) || yes(c.IsArchived) || len(c.PendingShared) > 0 || (c.ContextTeamID != "" && c.ContextTeamID != team) {
		return false
	}
	for _, id := range c.SharedTeamIDs {
		if id != team {
			return false
		}
	}
	if dm {
		return yes(c.IsIM) && !yes(c.IsChannel) && !yes(c.IsGroup) && c.User == user
	}
	return no(c.IsShared) && no(c.IsIM) && !yes(c.IsMPIM) && (yes(c.IsChannel) || yes(c.IsGroup)) && yes(c.IsMember) && c.IsPrivate != nil && c.IsArchived != nil
}
