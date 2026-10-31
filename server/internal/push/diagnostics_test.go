package push

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestAuditDisabledPusherDoesNotLogPrivateBody(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	p := Disabled{Log: logger}
	_ = p.Push(context.Background(), Message{Payload: []byte(`{"aps":{"alert":{"body":"AUDIT_PRIVATE_BODY_NOT_REAL"}}}`)})
	leaked := strings.Contains(buf.String(), "AUDIT_PRIVATE_BODY_NOT_REAL")
	t.Logf("push disabled + debug logs contain private notification body: %v", leaked)
	if leaked {
		t.Fatal("notification content escaped into debug diagnostics")
	}
}
