package database

import (
	"watgbridge/state"
)

// MessageReaction stores one row per (chat, message, sender): the sender's
// current emoji reaction. Re-reacting replaces the emoji (WhatsApp semantics);
// revoking (emoji = "") deletes the row.
type MessageReaction struct {
	WaChatId string `gorm:"primaryKey;column:wa_chat_id;index:idx_reactions_msg_chat"`
	WaMsgId  string `gorm:"primaryKey;column:wa_msg_id;index:idx_reactions_msg_chat"`
	Sender   string `gorm:"primaryKey;column:sender"`
	Emoji    string
}

// ReactionUpsert records the sender's reaction on a message. An empty emoji
// (reaction revoked on WhatsApp) removes the sender's row.
func ReactionUpsert(waChatId, waMsgId, senderId, emoji string) error {
	db := state.State.Database
	if emoji == "" {
		return db.Where("wa_chat_id = ? AND wa_msg_id = ? AND sender = ?", waChatId, waMsgId, senderId).
			Delete(&MessageReaction{}).Error
	}

	var existing MessageReaction
	res := db.Where("wa_chat_id = ? AND wa_msg_id = ? AND sender = ?", waChatId, waMsgId, senderId).
		First(&existing)
	if res.Error != nil {
		// Not found (or a real error). Distinguish by retrying a plain create;
		// a genuine DB failure will surface there too.
		return db.Create(&MessageReaction{
			WaChatId: waChatId,
			WaMsgId:  waMsgId,
			Sender:   senderId,
			Emoji:    emoji,
		}).Error
	}

	existing.Emoji = emoji
	return db.Save(&existing).Error
}

// ReactionCounts returns emoji -> count for a message.
func ReactionCounts(waChatId, waMsgId string) (map[string]int, error) {
	db := state.State.Database

	var rows []MessageReaction
	res := db.Where("wa_chat_id = ? AND wa_msg_id = ?", waChatId, waMsgId).Find(&rows)
	if res.Error != nil {
		return nil, res.Error
	}

	counts := make(map[string]int, len(rows))
	for _, r := range rows {
		counts[r.Emoji]++
	}
	return counts, nil
}

// ReactionRows returns the raw reaction rows (sender + emoji) for a message,
// in insertion order — used for the "who reacted with what" summary.
func ReactionRows(waChatId, waMsgId string) ([]MessageReaction, error) {
	db := state.State.Database

	var rows []MessageReaction
	res := db.Where("wa_chat_id = ? AND wa_msg_id = ?", waChatId, waMsgId).
		Order("rowid ASC").Find(&rows)
	if res.Error != nil {
		return nil, res.Error
	}
	return rows, nil
}

// ReactionGetSenderEmoji returns the emoji the sender currently has on the
// message, or "" when they have none.
func ReactionGetSenderEmoji(waChatId, waMsgId, senderId string) (string, error) {
	db := state.State.Database

	var row MessageReaction
	res := db.Where("wa_chat_id = ? AND wa_msg_id = ? AND sender = ?", waChatId, waMsgId, senderId).
		First(&row)
	if res.Error != nil {
		return "", nil // no reaction yet — not an error
	}
	return row.Emoji, nil
}

// MsgIdGetParticipant returns the WhatsApp-side sender JID stored for a
// bridged message (needed to build reaction message keys).
func MsgIdGetParticipant(waMsgId, waChatId string) (string, error) {
	db := state.State.Database

	var bridgePair MsgIdPair
	res := db.Where("id = ? AND wa_chat_id = ?", waMsgId, waChatId).First(&bridgePair)
	if res.Error == nil {
		return bridgePair.ParticipantId, nil
	}

	// Chat id may be stored under its LID form — fall back to the message id alone.
	res2 := db.Where("id = ?", waMsgId).First(&bridgePair)
	if res2.Error != nil {
		return "", res.Error
	}
	return bridgePair.ParticipantId, nil
}
