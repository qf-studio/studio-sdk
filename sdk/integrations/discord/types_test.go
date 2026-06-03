package discord

import (
	"encoding/json"
	"testing"
	"time"
)

func TestStripBotMention(t *testing.T) {
	tests := []struct {
		name     string
		botID    string
		content  string
		expected string
	}{
		{
			name:     "strip exact mention",
			botID:    "123456789",
			content:  "<@123456789> deploy the thing",
			expected: "deploy the thing",
		},
		{
			name:     "strip nickname mention",
			botID:    "123456789",
			content:  "<@!123456789> deploy the thing",
			expected: "deploy the thing",
		},
		{
			name:     "no mention",
			botID:    "123456789",
			content:  "deploy the thing",
			expected: "deploy the thing",
		},
		{
			name:     "different user mention preserved",
			botID:    "123456789",
			content:  "<@987654321> deploy the thing",
			expected: "<@987654321> deploy the thing",
		},
		{
			name:     "empty botID strips any leading mention",
			botID:    "",
			content:  "<@123456789> deploy the thing",
			expected: "deploy the thing",
		},
		{
			name:     "empty botID strips nickname mention",
			botID:    "",
			content:  "<@!123456789> deploy the thing",
			expected: "deploy the thing",
		},
		{
			name:     "mention only returns empty",
			botID:    "123456789",
			content:  "<@123456789>",
			expected: "",
		},
		{
			name:     "empty botID mention only returns empty",
			botID:    "",
			content:  "<@123456789>",
			expected: "",
		},
		{
			name:     "leading and trailing whitespace trimmed",
			botID:    "123456789",
			content:  "<@123456789>   hello   ",
			expected: "hello",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StripBotMention(tt.content, tt.botID)
			if got != tt.expected {
				t.Errorf("StripBotMention(%q, %q) = %q, want %q", tt.content, tt.botID, got, tt.expected)
			}
		})
	}
}

func TestGatewayEventParseMessageCreate(t *testing.T) {
	msg := MessageCreate{
		ID:        "msg1",
		ChannelID: "chan1",
		GuildID:   "guild1",
		Author:    User{ID: "user1", Username: "alice"},
		Content:   "hello world",
		Timestamp: time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC),
	}

	raw, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	event := GatewayEvent{
		Op: OpcodeDispatch,
		D:  json.RawMessage(raw),
		T:  strPtr("MESSAGE_CREATE"),
	}

	got, err := event.ParseMessageCreate()
	if err != nil {
		t.Fatalf("ParseMessageCreate: %v", err)
	}
	if got.ID != msg.ID {
		t.Errorf("ID = %q, want %q", got.ID, msg.ID)
	}
	if got.Author.Username != msg.Author.Username {
		t.Errorf("Author.Username = %q, want %q", got.Author.Username, msg.Author.Username)
	}
	if got.Content != msg.Content {
		t.Errorf("Content = %q, want %q", got.Content, msg.Content)
	}
}

func TestGatewayEventParseInteractionCreate(t *testing.T) {
	ic := InteractionCreate{
		ID:        "int1",
		Token:     "tok1",
		Type:      3,
		ChannelID: "chan1",
		Data:      InteractionData{CustomID: "execute_task"},
	}

	raw, err := json.Marshal(ic)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	event := GatewayEvent{
		Op: OpcodeDispatch,
		D:  json.RawMessage(raw),
		T:  strPtr("INTERACTION_CREATE"),
	}

	got, err := event.ParseInteractionCreate()
	if err != nil {
		t.Fatalf("ParseInteractionCreate: %v", err)
	}
	if got.ID != ic.ID {
		t.Errorf("ID = %q, want %q", got.ID, ic.ID)
	}
	if got.Data.CustomID != ic.Data.CustomID {
		t.Errorf("Data.CustomID = %q, want %q", got.Data.CustomID, ic.Data.CustomID)
	}
}

func TestGatewayEventParseMessageCreateInvalid(t *testing.T) {
	event := GatewayEvent{
		D: json.RawMessage(`not valid json`),
	}
	_, err := event.ParseMessageCreate()
	if err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

func TestGatewayEventParseInteractionCreateInvalid(t *testing.T) {
	event := GatewayEvent{
		D: json.RawMessage(`not valid json`),
	}
	_, err := event.ParseInteractionCreate()
	if err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

func TestConfigFields(t *testing.T) {
	cfg := Config{
		BotToken:        "tok",
		BotID:           "bot1",
		AllowedGuilds:   []string{"g1", "g2"},
		AllowedChannels: []string{"c1"},
	}
	if cfg.BotToken != "tok" {
		t.Errorf("BotToken = %q, want tok", cfg.BotToken)
	}
	if len(cfg.AllowedGuilds) != 2 {
		t.Errorf("AllowedGuilds len = %d, want 2", len(cfg.AllowedGuilds))
	}
}

func TestConstantsValues(t *testing.T) {
	if DiscordAPIURL != "https://discord.com/api/v10" {
		t.Errorf("DiscordAPIURL = %q", DiscordAPIURL)
	}
	if OpcodeIdentify != 2 {
		t.Errorf("OpcodeIdentify = %d, want 2", OpcodeIdentify)
	}
	if OpcodeHello != 10 {
		t.Errorf("OpcodeHello = %d, want 10", OpcodeHello)
	}
	if InteractionResponseDeferredUpdateMessage != 6 {
		t.Errorf("InteractionResponseDeferredUpdateMessage = %d, want 6", InteractionResponseDeferredUpdateMessage)
	}
	if MaxMessageLength != 2000 {
		t.Errorf("MaxMessageLength = %d, want 2000", MaxMessageLength)
	}
}

func TestComponentAndButtonJSON(t *testing.T) {
	comp := Component{
		Type: 1,
		Components: []Button{
			{Type: 2, Style: 1, Label: "Execute", CustomID: "execute_task"},
			{Type: 2, Style: 4, Label: "Cancel", CustomID: "cancel_task"},
		},
	}
	data, err := json.Marshal(comp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded Component
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(decoded.Components) != 2 {
		t.Errorf("Components len = %d, want 2", len(decoded.Components))
	}
	if decoded.Components[0].Label != "Execute" {
		t.Errorf("Label = %q, want Execute", decoded.Components[0].Label)
	}
}

func TestDefaultIntents(t *testing.T) {
	expected := IntentGuilds | IntentGuildMessages | IntentDirectMessages | IntentMessageContent
	if DefaultIntents != expected {
		t.Errorf("DefaultIntents = %d, want %d", DefaultIntents, expected)
	}
}

func strPtr(s string) *string { return &s }
