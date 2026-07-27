package model

import (
	"sort"
	"strings"

	mmmodel "github.com/mattermost/mattermost/server/public/model"
)

// AutomationContext is the runtime context built up during automation execution.
type AutomationContext struct {
	AutomationID string `json:"automation_id"`
	CreatedBy    string `json:"created_by"`
	// TriggerType is the automation's configured trigger type
	// (e.g. "message_posted"). Set by the executor; not serialized.
	TriggerType string                `json:"-"`
	Trigger     TriggerData           `json:"trigger"`
	Steps       map[string]StepOutput `json:"steps"`
	// Actions holds the full ordered action list for the automation.
	// Set by the executor once before execution starts; not serialized.
	Actions []Action `json:"-"`
}

// TriggerData holds the data from the event that triggered the automation.
// It uses safe sub-structs that expose only scalar fields needed by
// templates, preventing PII leakage and blocking method calls on
// live model objects.
type TriggerData struct {
	Post       *SafePost       `json:"post,omitempty"`
	Channel    *SafeChannel    `json:"channel,omitempty"`
	User       *SafeUser       `json:"user,omitempty"`
	Team       *SafeTeam       `json:"team,omitempty"`
	Schedule   *ScheduleInfo   `json:"schedule,omitempty"`
	Membership *MembershipInfo `json:"membership,omitempty"`

	// Populated by the message_posted trigger handler when the firing post is itself
	// a thread reply (event.Post.RootId != ""), which can only happen when
	// MessagePostedConfig.IncludeThreadReplies is on. Nil for any other
	// trigger type and for root-post fires.
	Thread *SafeThread `json:"thread,omitempty"`
}

const MaxThreadReplies = 60

type SafeThread struct {
	RootID    string     `json:"root_id"`
	PostCount int        `json:"post_count"`
	Messages  []SafePost `json:"messages,omitempty"`
	// Truncated reports whether NewSafeThread dropped older replies to fit
	// within MaxThreadReplies. When true, len(Messages) < PostCount and the
	// root post (Messages[0]) is preserved alongside the most recent
	// MaxThreadReplies replies. Templates and prompt builders should disclose
	// truncation to the model so it knows it is not seeing the full thread.
	Truncated bool `json:"truncated,omitempty"`
}

func (t *SafeThread) TranscriptDisplay() string {
	if t == nil || len(t.Messages) == 0 {
		return ""
	}
	var b strings.Builder
	for _, m := range t.Messages {
		b.WriteString(m.User.AuthorDisplay())
		b.WriteString(": ")
		b.WriteString(m.Message)
		b.WriteString("\n\n")
	}
	return strings.TrimSuffix(b.String(), "\n\n")
}

// MembershipInfo contains metadata about a membership change trigger firing.
type MembershipInfo struct {
	Action string `json:"action"` // "joined" or "left"
}

// ScheduleInfo contains metadata about a schedule trigger firing.
type ScheduleInfo struct {
	FiredAt  int64  `json:"fired_at"` // Unix ms when schedule fired
	Interval string `json:"interval"` // configured interval
}

// SafeTeam contains only the team fields needed for template rendering.
// Sensitive fields (Email, InviteId, AllowedDomains) are excluded.
type SafeTeam struct {
	Id               string `json:"id"`
	Name             string `json:"name"`
	DisplayName      string `json:"display_name"`
	DefaultChannelId string `json:"default_channel_id,omitempty"`
}

// SafePost contains only the post fields needed for template rendering.
// User is populated from the post author when available. Callers that have
// already resolved the full user may replace the ID-only fallback with a
// richer SafeUser.
type SafePost struct {
	Id        string   `json:"id"`
	ChannelId string   `json:"channel_id"`
	ThreadId  string   `json:"thread_id"`
	Message   string   `json:"message"`
	FileIds   []string `json:"file_ids,omitempty"`
	User      SafeUser `json:"user"`
	CreateAt  int64    `json:"create_at,omitempty"`
}

// SafeChannel contains only the channel fields needed for template rendering.
type SafeChannel struct {
	Id          string `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
}

// SafeUser contains only the user fields needed for template rendering.
// Sensitive fields (email, AuthData, password, NotifyProps) are excluded.
type SafeUser struct {
	Id          string `json:"id"`
	Username    string `json:"username"`
	FirstName   string `json:"first_name"`
	LastName    string `json:"last_name"`
	IsGuestUser bool   `json:"is_guest,omitempty"`
}

// IsGuest returns whether the user has the guest role.
func (u *SafeUser) IsGuest() bool {
	return u.IsGuestUser
}

// AuthorDisplay returns the display form used when referring to this user
// in prose (thread transcripts, prompt text, logs). Prefers
// "@username (First Last)" when both are known, falling back through the
// most specific identifier available, ending at the user ID or the literal
// "unknown" when nothing else is set. A nil receiver returns "unknown" so
// templates can call this without nil guards on optional User fields.
func (u *SafeUser) AuthorDisplay() string {
	if u == nil {
		return "unknown"
	}
	fullName := strings.TrimSpace(u.FirstName + " " + u.LastName)
	switch {
	case u.Username != "" && fullName != "":
		return "@" + u.Username + " (" + fullName + ")"
	case u.Username != "":
		return "@" + u.Username
	case fullName != "":
		return "(" + fullName + ")"
	case u.Id != "":
		return u.Id
	default:
		return "unknown"
	}
}

// NewSafePost creates a SafePost from a Mattermost Post.
func NewSafePost(p *mmmodel.Post, user *SafeUser) *SafePost {
	if p == nil {
		return nil
	}
	threadId := p.RootId
	if threadId == "" {
		threadId = p.Id
	}
	return &SafePost{
		Id:        p.Id,
		ChannelId: p.ChannelId,
		ThreadId:  threadId,
		Message:   p.Message,
		FileIds:   append([]string(nil), p.FileIds...),
		User:      safePostUser(p.UserId, user),
		CreateAt:  p.CreateAt,
	}
}

func safePostUser(userID string, user *SafeUser) SafeUser {
	if user != nil {
		return *user
	}
	return SafeUser{Id: userID}
}

// NewSafeThread builds a SafeThread from a Mattermost PostList. Messages
// are returned oldest first, sorted by CreateAt (ties broken by post Id
// for determinism) so callers do not have to rely on PostList.Order
// direction. userFor may be nil; when non-nil it is invoked at most once
// per distinct user ID and may itself return nil when the lookup fails —
// in that case the resulting SafePost.User retains the user ID so
// AuthorDisplay still renders something.
func NewSafeThread(list *mmmodel.PostList, rootID string, userFor func(userID string) *SafeUser) *SafeThread {
	if list == nil {
		return nil
	}
	st := &SafeThread{
		RootID:    rootID,
		PostCount: len(list.Posts),
	}
	if len(list.Posts) == 0 {
		return st
	}
	sorted := make([]*mmmodel.Post, 0, len(list.Posts))
	for _, p := range list.Posts {
		if p != nil {
			sorted = append(sorted, p)
		}
	}
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].CreateAt != sorted[j].CreateAt {
			return sorted[i].CreateAt < sorted[j].CreateAt
		}
		return sorted[i].Id < sorted[j].Id
	})
	st.PostCount = len(sorted)

	// When the thread exceeds the cap, keep the root post (oldest, sets
	// the topic the LLM needs for context) plus the most recent
	// MaxThreadReplies replies.
	if len(sorted) > MaxThreadReplies+1 {
		root := sorted[0]
		recent := sorted[len(sorted)-MaxThreadReplies:]
		sorted = append([]*mmmodel.Post{root}, recent...)
		st.Truncated = true
	}

	cache := make(map[string]*SafeUser, 5)
	resolveUser := func(uid string) *SafeUser {
		if uid == "" || userFor == nil {
			return nil
		}
		if u, ok := cache[uid]; ok {
			return u
		}
		u := userFor(uid)
		cache[uid] = u
		return u
	}

	messages := make([]SafePost, 0, len(sorted))
	for _, p := range sorted {
		sp := *NewSafePost(p, resolveUser(p.UserId))
		// Override ThreadId with the resolved root for consistency across
		// all messages in the thread (NewSafePost would otherwise set the
		// root post's ThreadId to its own Id).
		sp.ThreadId = rootID
		messages = append(messages, sp)
	}
	st.Messages = messages
	return st
}

// NewSafeChannel creates a SafeChannel from a Mattermost Channel.
func NewSafeChannel(c *mmmodel.Channel) *SafeChannel {
	if c == nil {
		return nil
	}
	return &SafeChannel{
		Id:          c.Id,
		Name:        c.Name,
		DisplayName: c.DisplayName,
	}
}

// NewSafeUser creates a SafeUser from a Mattermost User,
// stripping all sensitive fields.
func NewSafeUser(u *mmmodel.User) *SafeUser {
	if u == nil {
		return nil
	}
	return &SafeUser{
		Id:          u.Id,
		Username:    u.Username,
		FirstName:   u.FirstName,
		LastName:    u.LastName,
		IsGuestUser: u.IsGuest(),
	}
}

// NewSafeTeam creates a SafeTeam from a Mattermost Team, stripping all
// sensitive fields. A nil input yields a placeholder SafeTeam so templates
// referencing team fields render something visible instead of blank when a
// team lookup fails.
// Note: DefaultChannelId is not part of mmmodel.Team and must be populated
// separately by the caller (e.g. via API.GetChannelByName).
func NewSafeTeam(t *mmmodel.Team) *SafeTeam {
	if t == nil {
		return &SafeTeam{
			Name:        "[unknown team]",
			DisplayName: "[unknown team]",
		}
	}
	return &SafeTeam{
		Id:          t.Id,
		Name:        t.Name,
		DisplayName: t.DisplayName,
	}
}

// StepOutput captures the result of an executed action.
type StepOutput struct {
	PostID    string `json:"post_id"`
	ChannelID string `json:"channel_id"`
	Message   string `json:"message"`
	Truncated bool   `json:"truncated,omitempty"`
}
