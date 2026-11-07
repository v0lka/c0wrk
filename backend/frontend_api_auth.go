package backend

import (
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/providerauth"
	"github.com/v0lka/sp4rk/llm"
)

const (
	chatGPTSignInJoinTimeout  = providerAuthSignInJoinTimeout
	chatGPTSignInStartTimeout = providerAuthSignInStartTimeout
	chatGPTProviderName       = "chatgpt"
	chatgptAuthEventPending   = providerAuthEventPending
	chatgptAuthEventSuccess   = providerAuthEventSuccess
	chatgptAuthEventError     = providerAuthEventError
	chatgptAuthEventCancelled = providerAuthEventCancelled
)

type chatgptAuthState = providerAuthState
type chatGPTAuthManager = providerAuthManager

func (f *FrontendAPI) chatGPTAuthDescriptor() providerAuthDescriptor {
	return providerAuthDescriptor{
		providerName: chatGPTProviderName,
		displayName:  "ChatGPT",
		profile:      providerauth.ChatGPT,
		authMode: func() string {
			f.configMu.RLock()
			defer f.configMu.RUnlock()
			if f.config == nil {
				return config.ChatGPTAuthModeAPIKey
			}
			return chatGPTAuthModeOrAPIKey(f.config.LLM.ChatGPT.Auth.Mode)
		},
		emit: func(event providerAuthEvent) {
			f.emitChatGPTAuthState(ChatGPTAuthEventData{
				State: event.state, AuthURL: event.authURL, Error: event.err,
				Email: event.email, AccountID: event.accountID, ExpiresAt: event.expiresAt,
			})
		},
	}
}

func chatGPTAuthModeOrAPIKey(mode string) string {
	if mode == "" {
		return config.ChatGPTAuthModeAPIKey
	}
	return mode
}

// InitChatGPTAuth restores ChatGPT credentials without network I/O.
func (l *FrontendAPILifecycle) InitChatGPTAuth() {
	l.f.initProviderAuth(&l.f.chatgptAuth, l.f.chatGPTAuthDescriptor())
}

// StartChatGPTSignIn starts one cancellable browser OAuth flow.
func (f *FrontendAPI) StartChatGPTSignIn() (*ChatGPTSignInResponse, error) {
	authURL, err := f.startProviderSignIn(&f.chatgptAuth, f.chatGPTAuthDescriptor())
	if err != nil {
		return nil, err
	}
	return &ChatGPTSignInResponse{AuthURL: authURL}, nil
}

// CancelChatGPTSignIn cancels the in-flight sign-in; it is idempotent.
func (f *FrontendAPI) CancelChatGPTSignIn() error {
	return f.cancelProviderSignIn(&f.chatgptAuth)
}

// GetChatGPTAuthStatus returns secret-free account and flow state.
func (f *FrontendAPI) GetChatGPTAuthStatus() ChatGPTAuthStatusResponse {
	st := f.providerAuthStatus(&f.chatgptAuth, f.chatGPTAuthDescriptor())
	return ChatGPTAuthStatusResponse{
		SignedIn: st.signedIn, Email: st.email, AccountID: st.accountID,
		ExpiresAt: st.expiresAt, Mode: st.mode, InFlight: st.inFlight, LastError: st.lastError,
	}
}

// SignOutChatGPT clears credentials and withdraws only ChatGPT's token source.
func (f *FrontendAPI) SignOutChatGPT() error {
	return f.signOutProvider(&f.chatgptAuth, f.chatGPTAuthDescriptor())
}

// GetChatGPTModelPreset returns the offline Codex preset with fail-soft metadata.
func (f *FrontendAPI) GetChatGPTModelPreset() ChatGPTModelPresetResponse {
	var reg *llm.ModelRegistry
	if b := f.builder(); b != nil {
		reg = b.ModelRegistry()
	}
	models := make([]ChatGPTModelPresetEntry, 0, len(chatGPTModelPreset))
	for _, name := range chatGPTModelPreset {
		metadata := resolveSubscriptionModelMetadata(name, reg)
		models = append(models, ChatGPTModelPresetEntry{
			Name: name, ContextWindow: metadata.contextWindow,
			OutputLimit: metadata.outputLimit, Reasoning: metadata.reasoning,
		})
	}
	return ChatGPTModelPresetResponse{Models: models}
}

var chatGPTModelPreset = []string{
	"gpt-5.5",
	"gpt-5.4",
	"gpt-5.4-mini",
	"gpt-5.3-codex",
	"codex-mini-latest",
}

const (
	chatGPTModelsPath           = "/models"
	chatGPTCatalogClientVersion = "0.159.3"
)

type chatGPTModelsCatalog struct {
	Models []chatGPTCatalogModel `json:"models"`
}

type chatGPTCatalogModel struct {
	Slug             string `json:"slug"`
	Visibility       string `json:"visibility"`
	Priority         int    `json:"priority"`
	ContextWindow    *int64 `json:"context_window"`
	MaxContextWindow *int64 `json:"max_context_window"`
}

// FetchChatGPTModels returns the live per-account Codex catalog.
func (f *FrontendAPI) FetchChatGPTModels() (ChatGPTModelPresetResponse, error) {
	query := "?client_version=" + url.QueryEscape(chatGPTCatalogClientVersion)
	body, err := f.fetchSubscriptionCatalog(&f.chatgptAuth, f.chatGPTAuthDescriptor(), query, true)
	if err != nil {
		return ChatGPTModelPresetResponse{}, err
	}
	var catalog chatGPTModelsCatalog
	if err := json.Unmarshal(body, &catalog); err != nil {
		return ChatGPTModelPresetResponse{}, fmt.Errorf("fetching the ChatGPT model list: decoding the catalog: %w", err)
	}
	visible := make([]chatGPTCatalogModel, 0, len(catalog.Models))
	for _, cm := range catalog.Models {
		if cm.Slug == "" || cm.Visibility != "list" {
			continue
		}
		visible = append(visible, cm)
	}
	slices.SortStableFunc(visible, func(a, b chatGPTCatalogModel) int {
		if a.Priority != b.Priority {
			return a.Priority - b.Priority
		}
		return strings.Compare(a.Slug, b.Slug)
	})

	var reg *llm.ModelRegistry
	if b := f.builder(); b != nil {
		reg = b.ModelRegistry()
	}
	models := make([]ChatGPTModelPresetEntry, 0, len(visible))
	for _, cm := range visible {
		models = append(models, chatGPTCatalogEntry(cm, reg))
	}
	return ChatGPTModelPresetResponse{Models: models}, nil
}

func chatGPTCatalogEntry(cm chatGPTCatalogModel, reg *llm.ModelRegistry) ChatGPTModelPresetEntry {
	metadata := resolveSubscriptionModelMetadata(cm.Slug, reg)
	entry := ChatGPTModelPresetEntry{
		Name: cm.Slug, ContextWindow: metadata.contextWindow,
		OutputLimit: metadata.outputLimit, Reasoning: metadata.reasoning,
	}
	if entry.ContextWindow <= 0 {
		if cm.ContextWindow != nil && *cm.ContextWindow > 0 {
			entry.ContextWindow = int(*cm.ContextWindow)
		} else if cm.MaxContextWindow != nil && *cm.MaxContextWindow > 0 {
			entry.ContextWindow = int(*cm.MaxContextWindow)
		}
	}
	return entry
}

// emitChatGPTAuthState publishes secret-free flow transitions.
func (f *FrontendAPI) emitChatGPTAuthState(data ChatGPTAuthEventData) {
	f.seedAcquire()
	if f.emitEvent != nil {
		f.emitEvent(EventChatGPTAuthState, data)
	}
}
