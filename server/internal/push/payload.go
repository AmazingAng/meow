package push

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"meow.blue/meow/message"
	"meow.blue/meow/server/internal/store"
)

// Notification categories registered by the iOS app (UNNotificationCategory identifiers).
const (
	CategoryInfo     = "MEOW_INFO"     // no actions
	CategoryReply    = "MEOW_REPLY"    // UNTextInputNotificationAction
	CategoryApproval = "MEOW_APPROVAL" // Approve / Reject
	CategoryChoice   = "MEOW_CHOICE"   // custom choices, rendered by the content extension
)

const (
	maxAlertBody = message.MaxPreviewRunes // runes shown in the banner; the app shows the full text
	// Bundled by both iOS targets; older apps fall back to the system sound.
	NotificationSound = "meow.wav"
	// MaxPayload is the APNs limit for alert notifications.
	MaxPayload = 4096
)

func Category(n store.Notification) string {
	q := n.Question
	switch {
	case q == nil:
		return CategoryInfo
	case q.Choices == nil:
		return CategoryReply
	case isApproval(q.Choices):
		return CategoryApproval
	default:
		return CategoryChoice
	}
}

func isApproval(choices []string) bool {
	if len(choices) != 2 {
		return false
	}
	a, b := strings.ToLower(choices[0]), strings.ToLower(choices[1])
	return (a == "approve" && b == "reject") || (a == "reject" && b == "approve")
}

func interruptionLevel(priority string) string {
	switch priority {
	case "low":
		return "passive"
	case "high":
		return "time-sensitive"
	}
	return "active"
}

func truncate(s string, n int) string { return message.Truncate(s, n) }

// BuildMessage renders session, project/agent and body in the system's title,
// subtitle and body fields, matching the iOS relay presentation.
// The alert text is shortened until the serialized payload fits MaxPayload.
func BuildMessage(t store.PushTarget) (Message, error) {
	n := t.Notification
	title, subtitle := notificationHeadings(n)
	body := notificationPreview(n.Body)
	aps := map[string]any{
		"category":           Category(n),
		"mutable-content":    1, // lets the service extension report delivery
		"interruption-level": interruptionLevel(n.Priority),
	}
	if n.Priority != "low" {
		aps["sound"] = NotificationSound
	}
	aps["badge"] = t.Badge
	if n.Source != "" {
		aps["thread-id"] = n.Source
	}
	meow := map[string]any{"id": n.ID, "type": n.Type, "device_id": t.Device.ID, "preview_format": "plain"}
	expiration := n.CreatedAt.Add(24 * time.Hour)
	if q := n.Question; q != nil {
		meow["question_id"] = n.QuestionID
		meow["expires_at"] = q.ExpiresAt
		if q.Choices != nil {
			meow["choices"] = q.Choices
		}
		expiration = q.ExpiresAt
	}
	render := func(titleRunes, subtitleRunes, bodyRunes int) ([]byte, error) {
		aps["alert"] = map[string]string{"title": truncate(title, titleRunes), "subtitle": truncate(subtitle, subtitleRunes), "body": truncate(body, bodyRunes)}
		return json.Marshal(map[string]any{"aps": aps, "meow": meow})
	}

	titleRunes := utf8.RuneCountInString(title)
	subtitleRunes := utf8.RuneCountInString(subtitle)
	bodyRunes := min(utf8.RuneCountInString(body), maxAlertBody)
	payload, err := render(titleRunes, subtitleRunes, bodyRunes)
	if err == nil && len(payload) > MaxPayload {
		// Binary search the longest body that fits; if even an empty body is
		// too big, shorten the subtitle before the session title.
		if p, _ := render(titleRunes, subtitleRunes, 0); len(p) > MaxPayload {
			bodyRunes = 0
			if p, _ := render(titleRunes, 0, 0); len(p) > MaxPayload {
				subtitleRunes = 0
				titleRunes = longestFitting(titleRunes, func(n int) bool { p, _ := render(n, 0, 0); return len(p) <= MaxPayload })
			} else {
				subtitleRunes = longestFitting(subtitleRunes, func(n int) bool { p, _ := render(titleRunes, n, 0); return len(p) <= MaxPayload })
			}
		} else {
			bodyRunes = longestFitting(bodyRunes, func(n int) bool { p, _ := render(titleRunes, subtitleRunes, n); return len(p) <= MaxPayload })
		}
		payload, err = render(titleRunes, subtitleRunes, bodyRunes)
	}
	if err == nil && len(payload) > MaxPayload {
		err = fmt.Errorf("push payload is %d bytes, over the %d byte APNs limit", len(payload), MaxPayload)
	}
	return Message{
		Token:      t.Device.APNsToken,
		Sandbox:    t.Device.APNsEnv == "sandbox",
		Priority:   n.Priority,
		Expiration: expiration,
		Payload:    payload,
	}, err
}

// Keep the fallbacks aligned with NotificationOrigin.compactHeadline/Metadata.
// A sender without a session keeps its business title; an event with missing
// context gets a neutral Session label. Never infer event kinds from titles.
func notificationHeadings(n store.Notification) (title, subtitle string) {
	agent := strings.TrimSpace(n.Source)
	if agent == "" {
		agent = "Meow"
	}
	var workspace, session, id string
	if n.Context != nil {
		workspace = strings.TrimSpace(n.Context.Workspace)
		session = strings.TrimSpace(n.Context.Session)
		id = strings.TrimSpace(n.Context.SessionID)
	}
	title = n.Title
	switch {
	case session != "":
		title = session
	case id != "":
		runes := []rune(id)
		if len(runes) > 12 {
			id = string(runes[:8]) + "…" + string(runes[len(runes)-4:])
		}
		title = "Session · " + id
	case n.Event != "":
		title = "Session"
	case workspace != "" && strings.HasSuffix(title, " · "+workspace) && len(title) > len(" · "+workspace):
		title = strings.TrimSuffix(title, " · "+workspace)
	}
	subtitle = agent
	if workspace != "" {
		subtitle = workspace + " · " + agent
	}
	return title, subtitle
}

// longestFitting returns the largest n in [0, hi] for which fits(n) holds,
// assuming fits is monotonic (true up to some point, then false).
func longestFitting(hi int, fits func(int) bool) int {
	lo := 0
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if fits(mid) {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo
}
