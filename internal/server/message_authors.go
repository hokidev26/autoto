package server

import (
	"context"
	"net/http"
	"strings"

	"autoto/internal/db"
)

func attachMessageAuthors(ctx context.Context, store *db.Store, messages []db.Message) error {
	if store == nil || len(messages) == 0 {
		return nil
	}
	ids := make([]string, 0, len(messages))
	for _, message := range messages {
		ids = append(ids, message.CreatedBy)
	}
	authors, err := store.ListMessageAuthors(ctx, ids)
	if err != nil {
		return err
	}
	if len(authors) == 0 {
		return nil
	}
	for i := range messages {
		author, ok := authors[strings.TrimSpace(messages[i].CreatedBy)]
		if !ok {
			continue
		}
		copy := author
		messages[i].Author = &copy
	}
	return nil
}

func (s *Server) writeHydratedMessage(w http.ResponseWriter, r *http.Request, status int, message db.Message) {
	messages := []db.Message{message}
	if err := attachMessageAuthors(r.Context(), s.store, messages); err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, status, messages[0])
}
