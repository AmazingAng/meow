package adapters

import "meow.blue/meow/message"

// Services use the standard protocol directly; no agent-specific fields or
// inferred outcome are needed. Generic JSON always preserves the full body.
func decodeService(data []byte) (Event, bool, error) {
	n, err := message.Decode(data)
	return Event{Notification: n}, err == nil, err
}
