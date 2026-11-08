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
// buttons. Format: "react|<waChatId>|<waMsgId>|<emoji>".
const ReactionCallbackPrefix = "react|"

func ReactionCallbackData(waChatId, waMsgId, emoji string) string {
	return ReactionCallbackPrefix + waChatId + "|" + waMsgId + "|" + emoji
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

// reactionSenderButton rebuilds the original sender URL button for a bridged
// message (contact name -> wa.me link), matching TgBuildUrlButton usage in the
// WhatsApp handlers.
func reactionSenderButton(participantId string) (string, string) {
	if state.State.WhatsAppClient == nil {
		return "", ""
	}
	jid, err := types.ParseJID(participantId)
	if err != nil || jid.User == "" {
		return "", ""
	}
	return WaGetContactName(jid), fmt.Sprintf("https://wa.me/%s", jid.User)
}

// TgBuildReactionMarkup rebuilds a bridged message's inline keyboard:
// row 1 = the original sender URL button (preserved), then one button per
// emoji with its count. With zero reactions the markup collapses back to the
// sender button alone.
func TgBuildReactionMarkup(waChatId, waMsgId, participantId string) (gotgbot.InlineKeyboardMarkup, error) {
	counts, err := database.ReactionCounts(waChatId, waMsgId)
	if err != nil {
		return gotgbot.InlineKeyboardMarkup{}, err
	}

	markup := gotgbot.InlineKeyboardMarkup{InlineKeyboard: [][]gotgbot.InlineKeyboardButton{}}

	if name, url := reactionSenderButton(participantId); name != "" {
		markup.InlineKeyboard = append(markup.InlineKeyboard, []gotgbot.InlineKeyboardButton{{Text: name, Url: url}})
	}

	emojis := make([]string, 0, len(counts))
	for e := range counts {
		emojis = append(emojis, e)
	}
	// Busiest first, ties broken alphabetically for stable ordering.
	sort.Slice(emojis, func(i, j int) bool {
		if counts[emojis[i]] != counts[emojis[j]] {
			return counts[emojis[i]] > counts[emojis[j]]
		}
		return emojis[i] < emojis[j]
	})

	if len(emojis) > 0 {
		row := make([]gotgbot.InlineKeyboardButton, 0, len(emojis))
		for _, e := range emojis {
			row = append(row, gotgbot.InlineKeyboardButton{
				Text:         fmt.Sprintf("%s %d", e, counts[e]),
				CallbackData: ReactionCallbackData(waChatId, waMsgId, e),
			})
		}
		markup.InlineKeyboard = append(markup.InlineKeyboard, row)
	}

	return markup, nil
}

// TgUpdateReactions edits a bridged Telegram message's reply markup in place
// to reflect the reaction state currently stored in the database.
func TgUpdateReactions(tgChatId, tgMsgId int64, waChatId, waMsgId string) error {
	if state.State.TelegramBot == nil {
		return nil
	}

	participantId, _ := database.MsgIdGetParticipant(waMsgId, waChatId)
	markup, err := TgBuildReactionMarkup(waChatId, waMsgId, participantId)
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
