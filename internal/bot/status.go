package bot

import (
	"context"
	"sync"
	"time"
)

const (
	chatActionTyping         = "typing"
	chatActionUploadVideo    = "upload_video"
	chatActionUploadPhoto    = "upload_photo"
	chatActionUploadDocument = "upload_document"

	// Telegram shows a chat action for five seconds, so it is repeated a bit
	// more often to keep the status visible without gaps.
	chatActionInterval = 4 * time.Second
	chatActionTimeout  = 5 * time.Second
)

// chatStatus keeps a status such as "sending video..." visible while a job
// runs. It is purely cosmetic, so failures to send it are ignored.
type chatStatus struct {
	mu       sync.Mutex
	action   string
	stopOnce sync.Once
	stopped  chan struct{}
	done     chan struct{}
}

func (b *Bot) startChatStatus(ctx context.Context, item job, action string) *chatStatus {
	status := &chatStatus{action: action, stopped: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(status.done)
		ticker := time.NewTicker(chatActionInterval)
		defer ticker.Stop()
		for {
			sendCtx, cancel := context.WithTimeout(ctx, chatActionTimeout)
			if err := b.telegram.SendChatAction(sendCtx, item.chatID, item.threadID, status.current()); err != nil && ctx.Err() == nil {
				b.logger.Debug("cannot send chat action", "error", err, "chat_id", item.chatID)
			}
			cancel()
			select {
			case <-ticker.C:
			case <-status.stopped:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	return status
}

func (s *chatStatus) current() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.action
}

// set changes the action shown from the next repetition on.
func (s *chatStatus) set(action string) {
	s.mu.Lock()
	s.action = action
	s.mu.Unlock()
}

// stop ends the status and waits until no chat action is in flight, so a
// late action cannot follow the bot's reply.
func (s *chatStatus) stop() {
	s.stopOnce.Do(func() { close(s.stopped) })
	<-s.done
}
