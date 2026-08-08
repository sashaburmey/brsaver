package bot

import (
	"testing"

	"brsaver/internal/telegram"
)

func TestJobsForUpdateFiltersChatsAndLimitsLinks(t *testing.T) {
	t.Parallel()

	application := &Bot{config: Config{
		MaxLinksPerMessage: 1,
		AllowedChatIDs:     map[int64]struct{}{-1001: {}},
	}}
	update := telegram.Update{Message: &telegram.Message{
		MessageID: 15,
		Chat:      telegram.Chat{ID: -1001, Type: "supergroup"},
		Text:      "https://vm.tiktok.com/one/ https://youtube.com/shorts/two",
	}}

	jobs := application.jobsForUpdate(update)
	if len(jobs) != 1 {
		t.Fatalf("got %d jobs, want 1", len(jobs))
	}
	if jobs[0].chatID != -1001 || jobs[0].messageID != 15 || jobs[0].link.Provider != "TikTok" {
		t.Fatalf("unexpected job: %#v", jobs[0])
	}

	update.Message.Chat.ID = -1002
	if jobs := application.jobsForUpdate(update); len(jobs) != 0 {
		t.Fatalf("got jobs from disallowed chat: %#v", jobs)
	}
	update.Message.Chat = telegram.Chat{ID: -1001, Type: "private"}
	if jobs := application.jobsForUpdate(update); len(jobs) != 0 {
		t.Fatalf("got jobs from private chat: %#v", jobs)
	}
}
