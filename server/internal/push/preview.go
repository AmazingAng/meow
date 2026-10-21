package push

import "meow.blue/meow/message"

// Shared by direct delivery and the Go protocol contract. Swift mirrors its fixtures.
func notificationPreview(body string) string { return message.Preview(body) }
