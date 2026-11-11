package utils

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"watgbridge/database"
	"watgbridge/state"

	"github.com/PaulSonOfLars/gotgbot/v2"
	waCommon "go.mau.fi/whatsmeow/proto/waCommon"
	waE2E "go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// ReactionCallbackPrefix marks inline-keyboard callback data for reaction
// buttons. Data is "react|<emoji>" — the chat/message identity comes from the
// callback query's own message, keeping data far under Telegram's 64-byte cap.
const ReactionCallbackPrefix = "react|"

// ReactionInfoCallbackPrefix marks callback data for the "who reacted with
// what" button. Data is just the prefix — identity comes from the query.
const ReactionInfoCallbackPrefix = "reactinfo"

// DefaultReactions are the emoji buttons every bridged message ships with, so
// reactions are one tap away even before anyone reacts. Non-default emojis
// still get their own buttons when used.
var DefaultReactions = []string{"👍", "❤️", "😂", "😮", "🙏"}

func ReactionCallbackData(emoji string) string {
	return ReactionCallbackPrefix + emoji
}

func ReactionInfoCallbackData() string {
	return ReactionInfoCallbackPrefix
}

// WaNormalizeChatId resolves LID-form chat ids to their phone-number form so
// callback data matches what reaction events store in the database.
func WaNormalizeChatId(jid types.JID) string {
	if jid.Server == types.HiddenUserServer && state.State.WhatsAppClient != nil {
		if pn, err := state.State.WhatsAppClient.Store.LIDs.GetPNForLID(context.Background(), jid.ToNonAD()); err == nil {
			return pn.String()
		}
	}
	return jid.String()
}

// WaNormalizeSender resolves hidden/LID senders to their phone-number JID
// string so reaction state stays consistent between events and button clicks.
func WaNormalizeSender(sender types.JID) string {
	s := sender.ToNonAD()
	if s.Server == types.HiddenUserServer && state.State.WhatsAppClient != nil {
		if pn, err := state.State.WhatsAppClient.Store.LIDs.GetPNForLID(context.Background(), s); err == nil {
			return pn.ToNonAD().String()
		}
	}
	return s.String()
}

// reactionSenderName resolves a reaction sender JID string to a display name
// (contact name when known, "You" for the linked account, phone number as a
// fallback).
func reactionSenderName(senderJidStr string) string {
	jid, err := types.ParseJID(senderJidStr)
	if err != nil || jid.User == "" {
		return senderJidStr
	}
	if state.State.WhatsAppClient == nil || state.State.WhatsAppClient.Store == nil {
		return jid.User
	}
	return WaGetContactName(jid)
}

// TgBuildBridgedKeyboard builds the inline keyboard attached to every newly
// bridged message: one row with the default emoji buttons, so a reaction is a
// single tap even before anyone has reacted.
func TgBuildBridgedKeyboard() gotgbot.InlineKeyboardMarkup {
	row := make([]gotgbot.InlineKeyboardButton, 0, len(DefaultReactions))
	for _, e := range DefaultReactions {
		row = append(row, gotgbot.InlineKeyboardButton{
			Text:         e,
			CallbackData: ReactionCallbackData(e),
		})
	}
	return gotgbot.InlineKeyboardMarkup{InlineKeyboard: [][]gotgbot.InlineKeyboardButton{row}}
}

// TgBuildReactionMarkup rebuilds a bridged message's inline keyboard:
// row 1 = "who reacted with what" summary button (only once reactions exist;
// pressing it pops up sender names per emoji), then the default emoji buttons
// (with counts where present) followed by any non-default emojis used.
func TgBuildReactionMarkup(waChatId, waMsgId string) (gotgbot.InlineKeyboardMarkup, error) {
	counts, err := database.ReactionCounts(waChatId, waMsgId)
	if err != nil {
		return gotgbot.InlineKeyboardMarkup{}, err
	}

	markup := gotgbot.InlineKeyboardMarkup{InlineKeyboard: [][]gotgbot.InlineKeyboardButton{}}

	if len(counts) > 0 {
		markup.InlineKeyboard = append(markup.InlineKeyboard, []gotgbot.InlineKeyboardButton{{
			Text:         "🙂 Reactions",
			CallbackData: ReactionInfoCallbackData(),
		}})
	}

	// Default emojis first (fixed order), then extras — busiest first, ties
	// broken alphabetically for stable ordering.
	seen := make(map[string]bool, len(counts))
	rows := make([][]gotgbot.InlineKeyboardButton, 0, 2)

	row := make([]gotgbot.InlineKeyboardButton, 0, len(DefaultReactions))
	for _, e := range DefaultReactions {
		text := e
		if counts[e] > 0 {
			text = fmt.Sprintf("%s %d", e, counts[e])
		}
		row = append(row, gotgbot.InlineKeyboardButton{
			Text:         text,
			CallbackData: ReactionCallbackData(e),
		})
		seen[e] = true
	}
	rows = append(rows, row)

	extras := make([]string, 0, len(counts))
	for e := range counts {
		if !seen[e] {
			extras = append(extras, e)
		}
	}
	sort.Slice(extras, func(i, j int) bool {
		if counts[extras[i]] != counts[extras[j]] {
			return counts[extras[i]] > counts[extras[j]]
		}
		return extras[i] < extras[j]
	})

	if len(extras) > 0 {
		extraRow := make([]gotgbot.InlineKeyboardButton, 0, len(extras))
		for _, e := range extras {
			extraRow = append(extraRow, gotgbot.InlineKeyboardButton{
				Text:         fmt.Sprintf("%s %d", e, counts[e]),
				CallbackData: ReactionCallbackData(e),
			})
		}
		rows = append(rows, extraRow)
	}

	markup.InlineKeyboard = append(markup.InlineKeyboard, rows...)
	return markup, nil
}

// TgBuildReactionSummaryText renders the "who reacted with what" popup shown
// when the summary button is pressed: one line per emoji with sender names.
// Telegram caps callback answers at 200 characters — longer lists truncate.
func TgBuildReactionSummaryText(waChatId, waMsgId string) string {
	rows, err := database.ReactionRows(waChatId, waMsgId)
	if err != nil {
		return "Failed to load reactions"
	}
	if len(rows) == 0 {
		return "No reactions yet"
	}

	byEmoji := make(map[string][]string, len(rows))
	var emojiOrder []string
	for _, r := range rows {
		if _, ok := byEmoji[r.Emoji]; !ok {
			emojiOrder = append(emojiOrder, r.Emoji)
		}
		byEmoji[r.Emoji] = append(byEmoji[r.Emoji], reactionSenderName(r.Sender))
	}

	var sb strings.Builder
	for i, e := range emojiOrder {
		if i > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(e + " " + strings.Join(byEmoji[e], ", "))
	}

	text := sb.String()
	if runes := []rune(text); len(runes) > 200 {
		text = string(runes[:197]) + "..."
	}
	return text
}

// TgUpdateReactions edits a bridged Telegram message's reply markup in place
// to reflect the reaction state currently stored in the database.
func TgUpdateReactions(tgChatId, tgMsgId int64, waChatId, waMsgId string) error {
	if state.State.TelegramBot == nil {
		return nil
	}

	markup, err := TgBuildReactionMarkup(waChatId, waMsgId)
	if err != nil {
		return err
	}

	_, _, err = state.State.TelegramBot.EditMessageReplyMarkup(&gotgbot.EditMessageReplyMarkupOpts{
		ChatId:      tgChatId,
		MessageId:   tgMsgId,
		ReplyMarkup: markup,
	})
	if err != nil && strings.Contains(err.Error(), "message is not modified") {
		return nil // concurrent update produced the same keyboard
	}
	return err
}

// tgCantEditTarget reports whether Telegram refused the edit because the
// message belongs to a user (e.g. Dor's own TG→WA posts).
func tgCantEditTarget(err error) bool {
	return err != nil && strings.Contains(err.Error(), "message can't be edited")
}

// TgRefreshReactions refreshes the reaction display for a bridged message.
// On messages the bot owns it edits the keyboard in place; on user-authored
// messages (TG→WA posts) it falls back to the one-per-message reactions reply
// tracked in reaction_notices, updating that reply instead.
func TgRefreshReactions(tgChatId, tgThreadId, tgMsgId int64, waChatId, waMsgId string) error {
	err := TgUpdateReactions(tgChatId, tgMsgId, waChatId, waMsgId)
	if err == nil {
		return nil
	}
	if !tgCantEditTarget(err) {
		return err
	}
	return TgUpsertReactionNotice(tgChatId, tgThreadId, tgMsgId, waChatId, waMsgId)
}

// TgUpsertReactionNotice creates or refreshes the reactions reply under a
// user-authored Telegram message.
func TgUpsertReactionNotice(tgChatId, tgThreadId, tgMsgId int64, waChatId, waMsgId string) error {
	bot := state.State.TelegramBot
	if bot == nil {
		return nil
	}

	markup, err := TgBuildReactionMarkup(waChatId, waMsgId)
	if err != nil {
		return err
	}

	replyMsgId, err := database.ReactionNoticeGet(tgChatId, tgMsgId)
	if err != nil {
		return err
	}
	if replyMsgId != 0 {
		_, _, err = bot.EditMessageReplyMarkup(&gotgbot.EditMessageReplyMarkupOpts{
			ChatId:      tgChatId,
			MessageId:   replyMsgId,
			ReplyMarkup: markup,
		})
		if err == nil {
			return nil
		}
		if strings.Contains(err.Error(), "message is not modified") {
			return nil
		}
		if strings.Contains(err.Error(), "message to edit not found") {
			_ = database.ReactionNoticeDelete(tgChatId, tgMsgId) // recreate below
		} else {
			return err
		}
	}

	msg, err := bot.SendMessage(tgChatId, "🙂", &gotgbot.SendMessageOpts{
		ReplyParameters: &gotgbot.ReplyParameters{
			MessageId:                tgMsgId,
			AllowSendingWithoutReply: true,
		},
		MessageThreadId: tgThreadId,
		ReplyMarkup:     markup,
	})
	if err != nil {
		return err
	}
	return database.ReactionNoticeUpsert(tgChatId, tgMsgId, msg.MessageId)
}

// WaSendReaction sends (or revokes, emoji == "") a reaction on a WhatsApp
// message on behalf of the linked account — used when a Telegram reaction
// button is pressed.
func WaSendReaction(waChatId, waMsgId, participantId, emoji string) error {
	waClient := state.State.WhatsAppClient
	if waClient == nil {
		return fmt.Errorf("whatsapp client not connected")
	}
	chatJID, ok := WaParseJID(waChatId)
	if !ok {
		return fmt.Errorf("failed to parse chat JID %s", waChatId)
	}

	isFromMe := false
	if waClient.Store != nil && waClient.Store.ID != nil {
		self := waClient.Store.ID.ToNonAD()
		parsedParticipant, err := types.ParseJID(participantId)
		isFromMe = err == nil && parsedParticipant.User == self.User
	}

	key := &waCommon.MessageKey{
		RemoteJID: proto.String(waChatId),
		FromMe:    proto.Bool(isFromMe),
		ID:        proto.String(waMsgId),
	}
	if participantId != "" {
		key.Participant = proto.String(participantId)
	}

	_, err := waClient.SendMessage(context.Background(), chatJID, &waE2E.Message{
		ReactionMessage: &waE2E.ReactionMessage{
			Key:               key,
			Text:              proto.String(emoji),
			SenderTimestampMS: proto.Int64(time.Now().UnixMilli()),
		},
	})
	return err
}
