package federation

import (
	"context"
	"errors"
	"fmt"
	"git.duckfam.us/jj-software/cog/internal/httperr"
	"log/slog"
	"net/http"
	"strconv"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/user"
)

// OCSHandler implements the NC-compatible OCS remote_shares API.
type OCSHandler struct {
	Log    *slog.Logger
	Store  *Store
	Users  *user.Store
	Client *Client
	Policy *Policy

	// WriteOCS writes an OCS-formatted response. Injected to avoid importing compat.
	WriteOCS      func(w http.ResponseWriter, r *http.Request, status int, data any)
	WriteOCSError func(w http.ResponseWriter, r *http.Request, httpStatus, ocsCode int, message string)
}

type ocsRemoteShare struct {
	ID          int64  `json:"id"`
	Remote      string `json:"remote"`
	RemoteID    string `json:"remote_id"`
	ShareWith   string `json:"share_with"`
	ShareWithDN string `json:"share_with_displayname"`
	Name        string `json:"name"`
	Owner       string `json:"owner"`
	User        string `json:"user"`
	MountPoint  string `json:"mountpoint"`
	Accepted    bool   `json:"accepted"`
	Type        string `json:"type"`
	Permissions int    `json:"permissions"`
}

func (h *OCSHandler) fedToOCS(fs *FederatedShare) (*ocsRemoteShare, error) {
	ncID, err := h.Store.NCIDFor(fs.ID)
	if err != nil {
		return nil, err
	}

	// Resolve local user for display name.
	var displayName string
	u, err := h.Users.GetByID(fs.LocalUserID)
	switch {
	case errors.Is(err, user.ErrNotFound):
		// The local user is gone; the share is still listed, unnamed.
	case err != nil:
		return nil, fmt.Errorf("look up local user of federated share %s: %w", fs.ID, err)
	default:
		displayName = u.DisplayName
		if displayName == "" {
			displayName = u.Username
		}
	}

	return &ocsRemoteShare{
		ID:          ncID,
		Remote:      fs.RemoteHost,
		RemoteID:    fs.RemoteID,
		ShareWith:   displayName,
		ShareWithDN: displayName,
		Name:        fs.ResourceName,
		Owner:       fs.OwnerName + "@" + fs.OwnerHost,
		User:        fs.RemoteUser + "@" + fs.RemoteHost,
		MountPoint:  fs.MountPath,
		Accepted:    fs.State == "accepted",
		Type:        fs.ResourceType,
		Permissions: int(fs.Permissions),
	}, nil
}

// HandleListRemoteShares lists accepted inbound federated shares.
func (h *OCSHandler) HandleListRemoteShares(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		h.WriteOCSError(w, r, http.StatusUnauthorized, 997, "Unauthorized")
		return
	}

	var shares []ocsRemoteShare
	for fs, err := range h.Store.ListInbound(u.ID, "accepted") {
		if err != nil {
			httperr.Log(r, err)
			h.WriteOCSError(w, r, http.StatusInternalServerError, 500, "internal error")
			return
		}
		ocs, err := h.fedToOCS(&fs)
		if err != nil {
			httperr.Log(r, err)
			h.WriteOCSError(w, r, http.StatusInternalServerError, 500, "internal error")
			return
		}
		shares = append(shares, *ocs)
	}
	if shares == nil {
		shares = []ocsRemoteShare{}
	}
	h.WriteOCS(w, r, http.StatusOK, shares)
}

// HandleListPendingShares lists pending inbound federated shares.
func (h *OCSHandler) HandleListPendingShares(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		h.WriteOCSError(w, r, http.StatusUnauthorized, 997, "Unauthorized")
		return
	}

	var shares []ocsRemoteShare
	for fs, err := range h.Store.ListInbound(u.ID, "pending") {
		if err != nil {
			httperr.Log(r, err)
			h.WriteOCSError(w, r, http.StatusInternalServerError, 500, "internal error")
			return
		}
		ocs, err := h.fedToOCS(&fs)
		if err != nil {
			httperr.Log(r, err)
			h.WriteOCSError(w, r, http.StatusInternalServerError, 500, "internal error")
			return
		}
		shares = append(shares, *ocs)
	}
	if shares == nil {
		shares = []ocsRemoteShare{}
	}
	h.WriteOCS(w, r, http.StatusOK, shares)
}

// HandleAcceptShare accepts a pending inbound federated share.
func (h *OCSHandler) HandleAcceptShare(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		h.WriteOCSError(w, r, http.StatusUnauthorized, 997, "Unauthorized")
		return
	}

	fsID, err := h.resolveID(r.PathValue("id"))
	if err != nil {
		h.writeLookupError(w, r, err)
		return
	}

	fs, err := h.Store.Get(fsID)
	if err != nil {
		h.writeLookupError(w, r, err)
		return
	}
	if fs.LocalUserID != u.ID || fs.Direction != "inbound" || fs.State != "pending" {
		h.WriteOCSError(w, r, http.StatusNotFound, 404, "share not found")
		return
	}
	// Accepting tells the sender's server, so only a server this one
	// still federates with may be told.
	allowed, err := h.Policy.PeerAllowed(fs.RemoteHost)
	if err != nil {
		h.Log.Error("accept federated share: read federation policy", "err", err)
		h.WriteOCSError(w, r, http.StatusInternalServerError, 500, "internal error")
		return
	}
	if !allowed {
		h.WriteOCSError(w, r, http.StatusForbidden, 403, "federation with "+fs.RemoteHost+" is not allowed on this server")
		return
	}

	mountPath := "/remote-shares/" + fs.ResourceName
	if err := h.Store.Accept(fs.ID, mountPath); err != nil {
		httperr.Log(r, err)
		h.WriteOCSError(w, r, http.StatusInternalServerError, 500, "internal error")
		return
	}

	// Notify the remote server that we accepted.
	if h.Client != nil && fs.RemoteHost != "" {
		go h.Client.Notify(context.Background(), fs.RemoteHost, OCMNotification{
			NotificationType: NotifyShareAccepted,
			ResourceType:     fs.ResourceType,
			ProviderID:       fs.RemoteID,
			SharedSecret:     fs.Token,
		})
	}

	h.WriteOCS(w, r, http.StatusOK, []any{})
}

// HandleDeclineShare declines a pending inbound federated share.
func (h *OCSHandler) HandleDeclineShare(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		h.WriteOCSError(w, r, http.StatusUnauthorized, 997, "Unauthorized")
		return
	}

	fsID, err := h.resolveID(r.PathValue("id"))
	if err != nil {
		h.writeLookupError(w, r, err)
		return
	}

	fs, err := h.Store.Get(fsID)
	if err != nil {
		h.writeLookupError(w, r, err)
		return
	}
	if fs.LocalUserID != u.ID || fs.Direction != "inbound" || fs.State != "pending" {
		h.WriteOCSError(w, r, http.StatusNotFound, 404, "share not found")
		return
	}

	if err := h.Store.Reject(fs.ID); err != nil {
		httperr.Log(r, err)
		h.WriteOCSError(w, r, http.StatusInternalServerError, 500, "internal error")
		return
	}

	if h.Client != nil && fs.RemoteHost != "" {
		go h.Client.Notify(context.Background(), fs.RemoteHost, OCMNotification{
			NotificationType: NotifyShareDeclined,
			ResourceType:     fs.ResourceType,
			ProviderID:       fs.RemoteID,
			SharedSecret:     fs.Token,
		})
	}

	h.WriteOCS(w, r, http.StatusOK, []any{})
}

// HandleRemoveShare removes an accepted inbound federated share.
func (h *OCSHandler) HandleRemoveShare(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		h.WriteOCSError(w, r, http.StatusUnauthorized, 997, "Unauthorized")
		return
	}

	fsID, err := h.resolveID(r.PathValue("id"))
	if err != nil {
		h.writeLookupError(w, r, err)
		return
	}

	fs, err := h.Store.Get(fsID)
	if err != nil {
		h.writeLookupError(w, r, err)
		return
	}
	if fs.LocalUserID != u.ID || fs.Direction != "inbound" {
		h.WriteOCSError(w, r, http.StatusNotFound, 404, "share not found")
		return
	}

	if err := h.Store.Remove(fs.ID); err != nil {
		httperr.Log(r, err)
		h.WriteOCSError(w, r, http.StatusInternalServerError, 500, "internal error")
		return
	}

	if h.Client != nil && fs.RemoteHost != "" {
		go h.Client.Notify(context.Background(), fs.RemoteHost, OCMNotification{
			NotificationType: NotifyShareUnshared,
			ResourceType:     fs.ResourceType,
			ProviderID:       fs.RemoteID,
			SharedSecret:     fs.Token,
		})
	}

	h.WriteOCS(w, r, http.StatusOK, []any{})
}

// writeLookupError reports a failed share lookup: 404 when there is no such
// share, 500 otherwise.
func (h *OCSHandler) writeLookupError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, ErrNotFound) {
		h.WriteOCSError(w, r, http.StatusNotFound, 404, "share not found")
		return
	}
	h.Log.Error("federated share lookup", "err", err)
	httperr.Log(r, err)
	h.WriteOCSError(w, r, http.StatusInternalServerError, 500, "internal error")
}

func (h *OCSHandler) resolveID(idStr string) (string, error) {
	// Try as NC integer ID first, then as internal string ID.
	ncID, err := strconv.ParseInt(idStr, 10, 64)
	if err == nil {
		return h.Store.FedShareIDFromNC(ncID)
	}
	return idStr, nil
}
