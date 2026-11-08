package telegram

import (
	"strings"

	"watgbridge/database"
	"watgbridge/state"
	"watgbridge/utils"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"
)

// ReactionButtonCallbackHandler toggles the linked WhatsApp account's reaction
// on the target message when a reaction button is pressed on Telegram.
func ReactionButtonCallbackHandler(b *gotgbot.Bot, c *ext.Context) error {
	if !utils.TgUpdateIsAuthorized(b, c) {
		return nil
	}

	cq := c.CallbackQuery
	if cq == nil {
		return nil
	}

	parts := strings.SplitN(strings.TrimPrefix(cq.Data, utils.ReactionCallbackPrefix), "|", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		cq.Answer(b, &gotgbot.AnswerCallbackQueryOpts{Text: "Invalid reaction data", ShowAlert: true})
		return nil
	}
	waChatId, waMsgId, emoji := parts[0], parts[1], parts[2]

	waClient := state.State.WhatsAppClient
	if waClient == nil || waClient.Store == nil || waClient.Store.ID == nil {
		cq.Answer(b, &gotgbot.AnswerCallbackQueryOpts{Text: "WhatsApp not connected", ShowAlert: true})
		return nil
	}
	selfJid := waClient.Store.ID.ToNonAD().String()

	participantId, err := database.MsgIdGetParticipant(waMsgId, waChatId)
	if err != nil && participantId == "" {
		cq.Answer(b, &gotgbot.AnswerCallbackQueryOpts{Text: "Message not bridged", ShowAlert: true})
		return nil
	}

	// Toggle: pressing the same emoji again revokes the reaction.
	current, _ := database.ReactionGetSenderEmoji(waChatId, waMsgId, selfJid)
	newEmoji := emoji
	answer := "Reaction sent to WhatsApp"
	if current == emoji {
		newEmoji = ""
		answer = "Reaction removed"
	}

	if err := utils.WaSendReaction(waChatId, waMsgId, participantId, newEmoji); err != nil {
		cq.Answer(b, &gotgbot.AnswerCallbackQueryOpts{Text: "Failed to send reaction to WhatsApp", ShowAlert: true})
		return err
	}

	// Our own sends produce no echo event, so persist + refresh here.
	_ = database.ReactionUpsert(waChatId, waMsgId, selfJid, newEmoji)

	tgChatId, _, tgMsgId, err := database.MsgIdGetTgFromWa(waMsgId, waChatId)
	if err == nil && tgMsgId != 0 {
		_ = utils.TgUpdateReactions(tgChatId, tgMsgId, waChatId, waMsgId)
	}

	cq.Answer(b, &gotgbot.AnswerCallbackQueryOpts{Text: answer})
	return nil
}
