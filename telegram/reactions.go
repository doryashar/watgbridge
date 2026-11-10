package telegram

import (
	"strings"

	"watgbridge/database"
	"watgbridge/state"
	"watgbridge/utils"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"
	waTypes "go.mau.fi/whatsmeow/types"
)

// reactionTarget resolves the WhatsApp chat/message ids behind a callback by
// reading them off the message that carries the keyboard. Legacy keyboards
// (pre-2026-11-10) instead embedded the ids in the callback data itself.
func reactionTarget(cq *gotgbot.CallbackQuery, waChatId, waMsgId string) (string, string, bool) {
	if waChatId != "" && waMsgId != "" {
		return waChatId, waMsgId, true
	}

	msg := cq.Message
	if msg == nil {
		return "", "", false
	}
	chatId := msg.GetChat().Id
	msgId := msg.GetMessageId()
	threadId := int64(0)
	if m, isAccessible := msg.(*gotgbot.Message); isAccessible {
		threadId = m.MessageThreadId
	}

	waMsgId, _, waChatId, err := database.MsgIdGetWaFromTg(chatId, msgId, threadId)
	if err == nil && waMsgId != "" {
		return normalizeTarget(waChatId, waMsgId)
	}
	// Topic/thread id may be stored differently for some pairs — match chat+msg only.
	waMsgId, _, waChatId, err = database.MsgIdGetWaFromTgMessage(chatId, msgId)
	if err == nil && waMsgId != "" {
		return normalizeTarget(waChatId, waMsgId)
	}
	return "", "", false
}

// normalizeTarget resolves LID-form chat ids to phone-number form so toggles
// key the same rows that WhatsApp reaction events (which always normalize) do.
func normalizeTarget(waChatId, waMsgId string) (string, string, bool) {
	jid, err := waTypes.ParseJID(waChatId)
	if err != nil {
		return "", "", false
	}
	return utils.WaNormalizeChatId(jid), waMsgId, true
}

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
	var waChatId, waMsgId, emoji string
	if len(parts) == 3 && parts[0] != "" && parts[1] != "" && parts[2] != "" {
		// Legacy keyboard: ids embedded in the data.
		waChatId, waMsgId, emoji = parts[0], parts[1], parts[2]
	} else if len(parts) == 1 && parts[0] != "" {
		// Current keyboard: data carries only the emoji.
		emoji = parts[0]
		var ok bool
		if waChatId, waMsgId, ok = reactionTarget(cq, "", ""); !ok {
			cq.Answer(b, &gotgbot.AnswerCallbackQueryOpts{Text: "Message not bridged", ShowAlert: true})
			return nil
		}
	} else {
		cq.Answer(b, &gotgbot.AnswerCallbackQueryOpts{Text: "Invalid reaction data", ShowAlert: true})
		return nil
	}

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

// ReactionInfoCallbackHandler answers the "🙂 Reactions" button with a popup
// listing who reacted with what. Read-only — nothing is toggled or sent.
func ReactionInfoCallbackHandler(b *gotgbot.Bot, c *ext.Context) error {
	cq := c.CallbackQuery
	if cq == nil {
		return nil
	}

	waChatId, waMsgId, ok := reactionTarget(cq, "", "")
	if !ok {
		cq.Answer(b, &gotgbot.AnswerCallbackQueryOpts{Text: "Message not bridged", ShowAlert: true})
		return nil
	}

	cq.Answer(b, &gotgbot.AnswerCallbackQueryOpts{
		Text:      utils.TgBuildReactionSummaryText(waChatId, waMsgId),
		ShowAlert: true,
	})
	return nil
}
