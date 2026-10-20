package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"meow.blue/meow/cli/internal/setup"
)

func TestV1DoesNotCreateOrWaitForQuestions(t *testing.T) {
	t.Setenv("MEOW_SERVER", "http://127.0.0.1:1")
	t.Setenv("MEOW_KEY", "meow_fixture")
	for _, args := range [][]string{{"ask", "Deploy?", "--no-wait"}, {"wait", "qst_old"}} {
		out := captureOutput(t, "", func() {
			if code := run(args); code != exitUsage {
				t.Errorf("exit=%d", code)
			}
		})
		if !strings.Contains(out, "V1 is notification-only") {
			t.Fatalf("missing explanation: %s", out)
		}
	}
}

func TestPublishedSkillIsNotificationOnly(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "skills", "meow", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	skill := string(data)
	if !strings.HasPrefix(skill, "---\nname: meow\ndescription: ") {
		t.Fatal("skills.sh needs name and description frontmatter")
	}
	if setup.IsMeowSkill(data) {
		t.Fatal("the published skill must not carry the marker setup removes")
	}
	for _, old := range []string{"meow ask", "meow wait", "--choices", "reply from the lock screen", "answer from the lock screen"} {
		if strings.Contains(skill, old) {
			t.Errorf("skill still recommends %q", old)
		}
	}
	for _, want := range []string{"send --alert --priority high", "Never treat silence", "npx skills add AmazingAng/meow"} {
		if !strings.Contains(skill, want) {
			t.Errorf("skill is missing %q", want)
		}
	}
}
