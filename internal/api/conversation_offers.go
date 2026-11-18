package api

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"net/http"
	"openfox/internal/collaboration"
	"openfox/internal/conversations"
	"openfox/internal/directchat"
	"openfox/internal/idempotency"
)

func (h *Handler) conversationOffers(w http.ResponseWriter, r *http.Request) {
	auth, sel, role, ok := h.conversationAuth(w, r)
	if !ok {
		return
	}
	act := conversationActor(auth, role, sel.AuthorizationVersion)
	org := sel.OrganizationID
	id := r.PathValue("conversationID")
	rid := collaborationRequestID(w, r)
	if r.Method != "GET" && !requireConversationCSRF(w, r, auth) {
		return
	}
	var in conversations.OfferInput
	var decision struct {
		Accept *bool `json:"accept"`
	}
	key := r.Header.Get(idempotency.HeaderName)
	switch r.Method {
	case "POST":
		if key == "" || !idempotency.ValidKey(key) {
			writeError(w, r, 400, "IDEMPOTENCY_KEY_REQUIRED", "valid Idempotency-Key is required")
			return
		}
		if decodeJSON(w, r, &in) != nil {
			writeConversationError(w, r, conversations.ErrInvalidRequest)
			return
		}
	case "PUT":
		if decodeJSON(w, r, &decision) != nil || decision.Accept == nil {
			writeConversationError(w, r, conversations.ErrInvalidRequest)
			return
		}
	}
	var out any
	replay := false
	err := h.runConversationTransaction(r, sel, func(ctx context.Context, tx pgx.Tx) error {
		s := conversations.Service{}
		switch r.Method {
		case "GET":
			if id != "" {
				rows, err := s.OfferPeers(ctx, tx, org, id, act, r.URL.Query().Get("kind"), r.URL.Query().Get("q"))
				out = rows
				return err
			}
			rows, err := s.PendingOffers(ctx, tx, org, act)
			out = rows
			return err
		case "POST":
			if err := s.CheckActor(ctx, tx, org, act); err != nil {
				return err
			}
			claim, err := idempotency.Begin(ctx, tx, "conversation.offer", key, struct {
				Conversation string
				Input        conversations.OfferInput
			}{id, in})
			if err != nil {
				return err
			}
			if claim.Replay {
				replay = true
				return nil
			}
			oid, err := s.SendOffer(ctx, tx, org, id, act, in)
			if err != nil {
				return err
			}
			if err = directchat.DeliverOffer(ctx, tx, act.UserID, in.Recipient, in.Kind, oid); err != nil {
				return err
			}
			if err = recordConversationAudit(ctx, tx, rid, org, act, "conversation.offer_sent", "conversation_offer", oid, "", map[string]string{"grant_scope": in.Kind}); err != nil {
				return err
			}
			return idempotency.Complete(ctx, tx, claim, oid)
		case "PUT":
			oid := r.PathValue("offerID")
			changed, err := s.DecideOffer(ctx, tx, org, oid, act, *decision.Accept, rid)
			if err != nil || !changed {
				return err
			}
			action := "conversation.offer_declined"
			if *decision.Accept {
				action = "conversation.offer_accepted"
			}
			if err = directchat.OfferChanged(ctx, tx, oid); err != nil {
				return err
			}
			return recordConversationAudit(ctx, tx, rid, org, act, action, "conversation_offer", oid, "", nil)
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, idempotency.ErrConflict) || errors.Is(err, collaboration.ErrVersionConflict) {
			writeCollaborationError(w, r, err)
		} else {
			writeConversationError(w, r, err)
		}
		return
	}
	if r.Method == "GET" {
		writeJSON(w, 200, out)
		return
	}
	if replay {
		w.Header().Set(idempotency.ReplayedHeader, "true")
	}
	w.WriteHeader(204)
}
