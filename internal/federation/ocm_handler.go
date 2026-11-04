package federation

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/user"
)

func secretEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// maxOCMBody bounds an inbound share or notification, a small JSON object
// from a server nobody here has signed in.
const maxOCMBody = 64 << 10

// OCMHandler handles inbound OCM requests from remote servers.
type OCMHandler struct {
	Log     *slog.Logger
	Store   *Store
	Users   *user.Store
	BaseURL string // e.g. "https://cog.example.com"
	Policy  *Policy
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
	json.NewEncoder(w).Encode(resp)
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

	// Extract the shared secret token. Required: notifications cannot be
	// authenticated without one.
	token := ocm.Protocol.Options.SharedSecret
	if token == "" {
		http.Error(w, "missing sharedSecret", http.StatusBadRequest)
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
	json.NewEncoder(w).Encode(map[string]string{"id": fs.ID})
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

	switch notif.NotificationType {
	case NotifyShareAccepted:
		if fs.Direction == "outbound" {
			h.Store.UpdateState(fs.ID, "accepted")
			h.Log.Info("outbound share accepted", "id", fs.ID)
		}
	case NotifyShareDeclined:
		if fs.Direction == "outbound" {
			h.Store.UpdateState(fs.ID, "rejected")
			h.Log.Info("outbound share declined", "id", fs.ID)
		}
	case NotifyShareUnshared:
		h.Store.UpdateState(fs.ID, "removed")
		h.Log.Info("share unshared by remote", "id", fs.ID)
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
