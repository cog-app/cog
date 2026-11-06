package federation

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/user"
)

func secretEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// maxOCMBody bounds an inbound share or notification, a small JSON object
// from a server nobody here has signed in.
const maxOCMBody = 64 << 10

// MaxPendingInbound is how many shares may wait for one user's answer. An
// inbound share is accepted on the sender's word, so without a cap any
// server could fill a user's pending list without end.
const MaxPendingInbound = 100

// discoverTimeout bounds the question put to a sender's host. The sender is
// waiting on this request, and a host that takes longer is not one it
// would be safe to go on waiting for.
const discoverTimeout = 10 * time.Second

// OCMHandler handles inbound OCM requests from remote servers.
type OCMHandler struct {
	Log     *slog.Logger
	Store   *Store
	Users   *user.Store
	BaseURL string // e.g. "https://cog.example.com"
	Policy  *Policy
	// Client asks a sender's host whether it federates.
	Client *Client
}

// HandleOCMProvider serves /.well-known/ocm-provider.
func (h *OCMHandler) HandleOCMProvider(w http.ResponseWriter, r *http.Request) {
	enabled, err := h.Policy.Enabled()
	if err != nil {
		h.Log.Error("ocm provider: read federation policy", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	resp := OCMProviderResponse{
		Enabled:    enabled,
		APIVersion: "1.1",
		EndPoint:   h.BaseURL + "/ocm",
		ResourceTypes: []OCMResourceType{{
			Name:       "file",
			ShareTypes: []string{"user"},
			Protocols: map[string]string{
				"webdav": "/public.php/webdav/",
			},
		}},
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		h.Log.Error("ocm provider: write response", "err", err)
	}
}

// HandleReceiveShare processes POST /ocm/shares — a remote server sends us a share.
func (h *OCMHandler) HandleReceiveShare(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var ocm OCMShare
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxOCMBody)).Decode(&ocm); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	if ocm.ShareWith == "" || ocm.Name == "" {
		http.Error(w, "shareWith and name are required", http.StatusBadRequest)
		return
	}

	// Extract remote host from the owner field (user@host format).
	remoteHost := ""
	remoteUser := ocm.Owner
	if at := strings.LastIndex(ocm.Owner, "@"); at >= 0 {
		remoteUser = ocm.Owner[:at]
		remoteHost = ocm.Owner[at+1:]
	}

	allowed, err := h.Policy.PeerAllowed(remoteHost)
	if err != nil {
		h.Log.Error("federation share: read federation policy", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !allowed {
		h.Log.Warn("federation share from disallowed peer", "host", remoteHost)
		http.Error(w, "peer not allowed", http.StatusForbidden)
		return
	}

	// Extract local username from shareWith (may be "user" or "user@ourhost").
	localUsername := ocm.ShareWith
	if at := strings.Index(localUsername, "@"); at >= 0 {
		localUsername = localUsername[:at]
	}

	// Verify the user exists locally.
	u, err := h.Users.GetByUsername(localUsername)
	if err != nil {
		h.Log.Warn("federation share for unknown user", "shareWith", ocm.ShareWith, "err", err)
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}

	// Nothing proves who sent this, so a user's pending shares are capped:
	// past the cap a sender, genuine or not, waits for the user to answer
	// some before more are kept.
	pending, err := h.Store.CountInbound(u.ID, "pending")
	if err != nil {
		h.Log.Error("federation share: count pending", "user", u.ID, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if pending >= MaxPendingInbound {
		h.Log.Warn("federation share refused: too many pending", "to", localUsername, "from", ocm.Owner)
		http.Error(w, "too many pending shares for this user", http.StatusTooManyRequests)
		return
	}

	// Extract the shared secret token. Required: notifications cannot be
	// authenticated without one.
	token := ocm.Protocol.Options.SharedSecret
	if token == "" {
		http.Error(w, "missing sharedSecret", http.StatusBadRequest)
		return
	}

	// OCM 1.1 signs nothing, so the sender is whoever the request says. The
	// claimed host is at least asked whether it federates: a name that runs
	// no OCM provider, or one that has federation off, sent nothing.
	ctx, cancel := context.WithTimeout(r.Context(), discoverTimeout)
	defer cancel()
	provider, err := h.Client.Discover(ctx, remoteHost)
	if err != nil {
		h.Log.Warn("federation share from unverifiable host", "host", remoteHost, "err", err)
		http.Error(w, "sender could not be verified", http.StatusForbidden)
		return
	}
	if !provider.Enabled {
		h.Log.Warn("federation share from host with federation off", "host", remoteHost)
		http.Error(w, "sender does not federate", http.StatusForbidden)
		return
	}

	fs := &FederatedShare{
		Direction:    "inbound",
		State:        "pending",
		RemoteID:     ocm.ProviderID,
		RemoteHost:   remoteHost,
		RemoteUser:   remoteUser,
		LocalUserID:  u.ID,
		Token:        token,
		ResourceName: ocm.Name,
		ResourceType: ocm.ResourceType,
		OwnerName:    ocm.OwnerDisplayName,
		OwnerHost:    remoteHost,
		Permissions:  1, // default read; could parse from protocol options
	}

	if err := h.Store.Create(fs); err != nil {
		h.Log.Error("create inbound federated share", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	h.Log.Info("received federated share",
		"from", ocm.Owner, "to", localUsername, "resource", ocm.Name, "id", fs.ID)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(w).Encode(map[string]string{"id": fs.ID}); err != nil {
		h.Log.Error("federation share: write response", "err", err)
	}
}

// HandleNotification processes POST /ocm/notifications — state changes from remote.
func (h *OCMHandler) HandleNotification(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var notif OCMNotification
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxOCMBody)).Decode(&notif); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	if notif.ProviderID == "" || notif.SharedSecret == "" {
		http.Error(w, "providerId and sharedSecret required", http.StatusBadRequest)
		return
	}

	fs, err := h.Store.Get(notif.ProviderID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		h.Log.Error("federation notification: look up share", "id", notif.ProviderID, "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if err != nil {
		// Don't leak whether the share existed.
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	if !secretEqual(fs.Token, notif.SharedSecret) {
		h.Log.Warn("federation notification with bad secret", "id", fs.ID, "type", notif.NotificationType)
		http.Error(w, "invalid sharedSecret", http.StatusForbidden)
		return
	}

	state := ""
	switch notif.NotificationType {
	case NotifyShareAccepted:
		if fs.Direction == "outbound" {
			state = "accepted"
		}
	case NotifyShareDeclined:
		if fs.Direction == "outbound" {
			state = "rejected"
		}
	case NotifyShareUnshared:
		state = "removed"
	}
	if state != "" {
		if err := h.Store.UpdateState(fs.ID, state); err != nil {
			h.Log.Error("federation notification: update share", "id", fs.ID, "state", state, "err", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		h.Log.Info("federated share state changed by remote", "id", fs.ID, "state", state)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(map[string]string{"status": "ok"}); err != nil {
		h.Log.Error("federation notification: write response", "err", err)
	}
}
