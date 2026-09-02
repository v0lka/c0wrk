package backend

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/v0lka/c0wrk/backend/session"
)

// AddBookmark persists a chat-event bookmark for a session and returns the
// created record. Bookmarks are isolated per session: the event_key is opaque
// to the backend and is produced/consumed by the frontend's groupMessages
// (DisplayItem key), so navigation and preview stay a frontend concern.
func (f *FrontendAPI) AddBookmark(sessionID, eventKey, title string) (session.SessionBookmark, error) {
	if f.store == nil {
		return session.SessionBookmark{}, errors.New("session store not initialized")
	}
	eventKey = strings.TrimSpace(eventKey)
	if eventKey == "" {
		return session.SessionBookmark{}, errors.New("bookmark event key is empty")
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = eventKey
	}

	bookmark := session.SessionBookmark{
		SessionID: sessionID,
		EventKey:  eventKey,
		Title:     title,
	}
	saved, err := f.store.SaveBookmark(context.Background(), bookmark)
	if err != nil {
		return session.SessionBookmark{}, fmt.Errorf("failed to add bookmark: %w", err)
	}
	return saved, nil
}

// ListBookmarks returns all bookmarks for a session, oldest first.
func (f *FrontendAPI) ListBookmarks(sessionID string) ([]session.SessionBookmark, error) {
	if f.store == nil {
		return nil, errors.New("session store not initialized")
	}
	bookmarks, err := f.store.ListBookmarks(context.Background(), sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to list bookmarks: %w", err)
	}
	return bookmarks, nil
}

// DeleteBookmark removes a bookmark from a session. sessionID scopes the delete
// so a bookmark id can never remove another session's bookmark.
func (f *FrontendAPI) DeleteBookmark(sessionID, bookmarkID string) error {
	if f.store == nil {
		return errors.New("session store not initialized")
	}
	if err := f.store.DeleteBookmark(context.Background(), sessionID, bookmarkID); err != nil {
		return fmt.Errorf("failed to delete bookmark: %w", err)
	}
	return nil
}

// RenameBookmark updates a bookmark's title. sessionID scopes the update so a
// bookmark id can never rename another session's bookmark.
func (f *FrontendAPI) RenameBookmark(sessionID, bookmarkID, title string) error {
	if f.store == nil {
		return errors.New("session store not initialized")
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return errors.New("bookmark title is empty")
	}
	if err := f.store.RenameBookmark(context.Background(), sessionID, bookmarkID, title); err != nil {
		return fmt.Errorf("failed to rename bookmark: %w", err)
	}
	return nil
}
