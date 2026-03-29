// Package federation implements Open Cloud Mesh (OCM) federated sharing
// between Cog instances and NextCloud servers.
package federation

// FederatedShare represents an inbound or outbound federated share.
type FederatedShare struct {
	ID           string
	Direction    string // "outbound" or "inbound"
	State        string // "pending", "accepted", "rejected", "removed"
	ShareID      *string
	RemoteID     string
	RemoteHost   string
	RemoteUser   string
	LocalUserID  string
	Token        string
	ResourceName string
	ResourceType string
	OwnerName    string
	OwnerHost    string
	Permissions  uint32
	MountPath    string
	CreatedAt    int64
	UpdatedAt    int64
}

// OCMShare is the payload for POST /ocm/shares (inbound from remote).
type OCMShare struct {
	ShareWith         string      `json:"shareWith"`
	Name              string      `json:"name"`
	Description       string      `json:"description"`
	ProviderID        string      `json:"providerId"`
	Owner             string      `json:"owner"`
	OwnerDisplayName  string      `json:"ownerDisplayName"`
	Sender            string      `json:"sender"`
	SenderDisplayName string      `json:"senderDisplayName"`
	ShareType         string      `json:"shareType"`
	ResourceType      string      `json:"resourceType"`
	Protocol          OCMProtocol `json:"protocol"`
}

// OCMProtocol describes how to access the shared resource.
type OCMProtocol struct {
	Name    string             `json:"name"`
	Options OCMProtocolOptions `json:"options"`
}

// OCMProtocolOptions holds WebDAV access details for a federated share.
type OCMProtocolOptions struct {
	SharedSecret string `json:"sharedSecret"`
	Permissions  string `json:"permissions"`
	WebDAVURL    string `json:"webDAV"`
}

// OCMProviderResponse is returned by /.well-known/ocm-provider.
type OCMProviderResponse struct {
	Enabled       bool              `json:"enabled"`
	APIVersion    string            `json:"apiVersion"`
	EndPoint      string            `json:"endPoint"`
	ResourceTypes []OCMResourceType `json:"resourceTypes"`
}

// OCMResourceType describes a federatable resource.
type OCMResourceType struct {
	Name       string            `json:"name"`
	ShareTypes []string          `json:"shareTypes"`
	Protocols  map[string]string `json:"protocols"`
}

// OCMNotification is sent between servers to communicate share state changes.
type OCMNotification struct {
	NotificationType string `json:"notificationType"`
	ResourceType     string `json:"resourceType"`
	ProviderID       string `json:"providerId"`
	Notification     any    `json:"notification"`
}

// Notification type constants.
const (
	NotifyShareAccepted = "SHARE_ACCEPTED"
	NotifyShareDeclined = "SHARE_DECLINED"
	NotifyShareUnshared = "SHARE_UNSHARED"
)
