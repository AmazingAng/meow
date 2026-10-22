package api

import (
	"time"

	"meow.blue/meow/message"
	"meow.blue/meow/server/internal/store"
)

type deviceJSON struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	APNsEnv      string    `json:"apns_env"`
	HasAPNsToken bool      `json:"has_apns_token"`
	CreatedAt    time.Time `json:"created_at"`
	LastSeenAt   time.Time `json:"last_seen_at"`
}

func toDeviceJSON(d store.Device) deviceJSON {
	return deviceJSON{
		ID: d.ID, Name: d.Name, APNsEnv: d.APNsEnv, HasAPNsToken: d.APNsToken != "",
		CreatedAt: d.CreatedAt, LastSeenAt: d.LastSeenAt,
	}
}

type keyJSON struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Hint       string     `json:"hint"`
	Scopes     []string   `json:"scopes"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	Secret     string     `json:"secret,omitempty"` // only in the response that creates the key
}

func toKeyJSON(k store.APIKey, secret string) keyJSON {
	return keyJSON{
		ID: k.ID, Name: k.Name, Hint: k.Hint, Scopes: k.Scopes,
		CreatedAt: k.CreatedAt, LastUsedAt: k.LastUsedAt, Secret: secret,
	}
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

type questionJSON struct {
	ID             string     `json:"id"`
	Status         string     `json:"status"`
	Question       string     `json:"question"`
	Choices        []string   `json:"choices"`
	Answer         *string    `json:"answer"`
	CreatedAt      time.Time  `json:"created_at"`
	ExpiresAt      time.Time  `json:"expires_at"`
	ResolvedAt     *time.Time `json:"resolved_at"`
	NotificationID string     `json:"notification_id"`
}

func toQuestionJSON(q store.Question) questionJSON {
	return questionJSON{
		ID: q.ID, Status: q.Status, Question: q.Text, Choices: q.Choices, Answer: optional(q.Answer),
		CreatedAt: q.CreatedAt, ExpiresAt: q.ExpiresAt, ResolvedAt: q.ResolvedAt, NotificationID: q.NotificationID,
	}
}

type notificationJSON = message.Record

func toNotificationJSON(n store.Notification) notificationJSON {
	out := notificationJSON{
		Event: n.Event, ID: n.ID, Type: n.Type, Title: n.Title, Body: n.Body, Source: n.Source, Priority: n.Priority,
		PushStatus: n.PushStatus, PushError: n.PushError, Context: n.Context,
		CreatedAt: n.CreatedAt, DeliveredAt: n.DeliveredAt, ReadAt: n.ReadAt,
	}
	if q := n.Question; q != nil {
		out.Question = &message.QuestionState{
			ID: n.QuestionID, Status: q.Status, Choices: q.Choices, Answer: optional(q.Answer), ExpiresAt: q.ExpiresAt,
		}
	}
	return out
}
