//lint:file-ignore SA1019 This test verifies compatibility isolation for Eino's deprecated MultiContent field.
package agentkit

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestAgentHistoryDeeplyIsolatesLegacyMultiContent(t *testing.T) {
	agent, err := New(context.Background(), &Config{Model: NewMockChatModel()})
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()

	source := []*schema.Message{{
		Role: schema.User,
		MultiContent: []schema.ChatMessagePart{{
			Type: schema.ChatMessagePartTypeImageURL,
			ImageURL: &schema.ChatMessageImageURL{
				URL:   "https://example.com/original.png",
				Extra: map[string]any{"labels": []string{"original"}},
			},
		}},
	}}
	agent.SetHistory(source)

	source[0].MultiContent[0].ImageURL.URL = "https://example.com/source.png"
	source[0].MultiContent[0].ImageURL.Extra["labels"].([]string)[0] = "source"
	history := agent.History()
	if got := history[0].MultiContent[0].ImageURL.URL; got != "https://example.com/original.png" {
		t.Fatalf("History() legacy URL = %q, want original", got)
	}
	if got := history[0].MultiContent[0].ImageURL.Extra["labels"].([]string)[0]; got != "original" {
		t.Fatalf("History() legacy labels = %q, want original", got)
	}

	history[0].MultiContent[0].ImageURL.URL = "https://example.com/snapshot.png"
	history[0].MultiContent[0].ImageURL.Extra["labels"].([]string)[0] = "snapshot"
	history = agent.History()
	if got := history[0].MultiContent[0].ImageURL.URL; got != "https://example.com/original.png" {
		t.Fatalf("second History() legacy URL = %q, want original", got)
	}
	if got := history[0].MultiContent[0].ImageURL.Extra["labels"].([]string)[0]; got != "original" {
		t.Fatalf("second History() legacy labels = %q, want original", got)
	}
}
