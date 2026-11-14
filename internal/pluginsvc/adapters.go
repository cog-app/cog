// Package pluginsvc adapts the host's stores to the interfaces plugins see
// through their PluginContext. The registry is given them before any
// plugin registers, so what a plugin reads during Register is there.
package pluginsvc

import (
	"fmt"
	"iter"
	"log/slog"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/email"
	"git.duckfam.us/jj-software/cog/internal/group"
	internalplugin "git.duckfam.us/jj-software/cog/internal/plugin"
	"git.duckfam.us/jj-software/cog/internal/share"
	"git.duckfam.us/jj-software/cog/internal/user"
	"git.duckfam.us/jj-software/cog/internal/volume"
	"git.duckfam.us/jj-software/cog/plugin"
)

// ShareStoreAdapter adapts *share.Store to plugin.ShareStore.
type ShareStoreAdapter struct {
	S *share.Store
}

func (a *ShareStoreAdapter) ListBySubject(subjectID string) iter.Seq2[plugin.ShareRecord, error] {
	return func(yield func(plugin.ShareRecord, error) bool) {
		for rec, err := range a.S.ListBySubject(subjectID) {
			if !yield(Share(&rec), err) {
				return
			}
		}
	}
}

// Share is a share record as plugins see it.
func Share(rec *share.Record) plugin.ShareRecord {
	return plugin.ShareRecord{
		ID:           rec.ID,
		OwnerUserID:  rec.OwnerUserID,
		ResourceType: rec.ResourceType,
		ResourceID:   rec.ResourceID,
		SubjectType:  rec.SubjectType,
		SubjectID:    rec.SubjectID,
		Permissions:  rec.Permissions,
	}
}

func (a *ShareStoreAdapter) CheckAccess(userID, resourceType, resourceID string) (*plugin.AccessDecision, error) {
	dec, err := a.S.CheckAccess(userID, resourceType, resourceID)
	if err != nil {
		return nil, err
	}
	return &plugin.AccessDecision{
		Allowed:         dec.Allowed,
		EffectivePerms:  dec.EffectivePerms,
		MatchedShareIDs: dec.MatchedShareIDs,
		OwnerUserID:     dec.OwnerUserID,
	}, nil
}

func (a *ShareStoreAdapter) Create(ownerUserID, resourceType, resourceID, subjectType, subjectID string, permissions uint32) (*plugin.ShareRecord, error) {
	rec, err := a.S.Create(ownerUserID, resourceType, resourceID, subjectType, subjectID, permissions)
	if err != nil {
		return nil, err
	}
	return &plugin.ShareRecord{
		ID:           rec.ID,
		OwnerUserID:  rec.OwnerUserID,
		ResourceType: rec.ResourceType,
		ResourceID:   rec.ResourceID,
		SubjectType:  rec.SubjectType,
		SubjectID:    rec.SubjectID,
		Permissions:  rec.Permissions,
	}, nil
}

func (a *ShareStoreAdapter) Decline(id, userID string) error {
	return a.S.Decline(id, userID)
}

func (a *ShareStoreAdapter) Delete(id string) error {
	return a.S.Delete(id)
}

// EmailSenderAdapter adapts a dynamic email.Sender provider to plugin.EmailSender.
type EmailSenderAdapter struct {
	SenderFunc func() *email.Sender
}

func (a *EmailSenderAdapter) Send(to []string, subject, body string, attachments ...plugin.EmailAttachment) error {
	return a.send(email.Message{To: to, Subject: subject, Text: body}, attachments)
}

func (a *EmailSenderAdapter) SendWithAttachments(to, subject, textBody, htmlBody string, attachments []plugin.EmailAttachment) error {
	return a.send(email.Message{To: []string{to}, Subject: subject, Text: textBody, HTML: htmlBody}, attachments)
}

func (a *EmailSenderAdapter) send(m email.Message, attachments []plugin.EmailAttachment) error {
	s := a.SenderFunc()
	if s == nil {
		return fmt.Errorf("SMTP not configured")
	}
	for _, att := range attachments {
		m.Attachments = append(m.Attachments, email.Attachment{
			Filename:    att.Filename,
			ContentType: att.ContentType,
			Data:        att.Data,
		})
	}
	return s.SendMessage(m)
}

// userLookupAdapter adapts *user.Store to plugin.UserLookup.
type userLookupAdapter struct {
	s *user.Store
}

func (a *userLookupAdapter) GetByID(id string) (*plugin.UserInfo, error) {
	u, err := a.s.GetByID(id)
	if err != nil {
		return nil, err
	}
	return &plugin.UserInfo{
		ID:          u.ID,
		Username:    u.Username,
		DisplayName: u.DisplayName,
		IsAdmin:     u.IsAdmin,
	}, nil
}

func (a *userLookupAdapter) GetByUsername(username string) (*plugin.UserInfo, error) {
	u, err := a.s.GetByUsername(username)
	if err != nil {
		return nil, err
	}
	return &plugin.UserInfo{
		ID:          u.ID,
		Username:    u.Username,
		DisplayName: u.DisplayName,
		IsAdmin:     u.IsAdmin,
	}, nil
}

// groupStoreAdapter adapts *group.Store to plugin.GroupStore.
type groupStoreAdapter struct {
	s *group.Store
}

func (a *groupStoreAdapter) ListGroupsForUser(userID string) iter.Seq2[plugin.GroupInfo, error] {
	return func(yield func(plugin.GroupInfo, error) bool) {
		for g, err := range a.s.ListGroupsForUser(userID) {
			pg := plugin.GroupInfo{
				ID:          g.ID,
				Name:        g.Name,
				MemberCount: g.MemberCount,
			}
			if !yield(pg, err) {
				return
			}
		}
	}
}

// volumeAccessAdapter adapts *volume.Access to plugin.VolumeAccess.
type volumeAccessAdapter struct {
	access *volume.Access
}

func (a *volumeAccessAdapter) ListAccessible(userID string) ([]plugin.VolumeInfo, error) {
	var result []plugin.VolumeInfo
	for v, err := range a.access.ListAccessible(userID) {
		if err != nil {
			return nil, err
		}
		scheme := ""
		if parsed, err := volume.ParseURI(v.URI); err != nil {
			slog.Warn("volume has an unparsable URI", "volume", v.ID, "err", err)
		} else {
			scheme = parsed.Scheme
		}
		result = append(result, plugin.VolumeInfo{
			ID:          v.ID,
			Name:        v.Name,
			Description: v.Description,
			Scheme:      scheme,
			IsSystem:    v.OwnerUserID == nil,
		})
	}
	return result, nil
}

// Build constructs the adapter layer from internal services.
func Build(shares *share.Store, emailFunc func() *email.Sender, users *user.Store, groups *group.Store, qc internalplugin.QuotaChecker, access *volume.Access, activityLog internalplugin.ActivityLogger, cookies plugin.CookieConfig, baseURL string) internalplugin.RegistryServices {
	var ss plugin.ShareStore
	if shares != nil {
		ss = &ShareStoreAdapter{S: shares}
	}
	var es plugin.EmailSender
	if emailFunc != nil {
		es = &EmailSenderAdapter{SenderFunc: emailFunc}
	}
	var ul plugin.UserLookup
	if users != nil {
		ul = &userLookupAdapter{users}
	}
	var gs plugin.GroupStore
	if groups != nil {
		gs = &groupStoreAdapter{groups}
	}
	var va plugin.VolumeAccess
	if access != nil {
		va = &volumeAccessAdapter{access}
	}
	return internalplugin.RegistryServices{
		ShareStore:   ss,
		EmailSender:  es,
		UserLookup:   ul,
		GroupStore:   gs,
		QuotaChecker: qc,
		VolumeAccess: va,
		ActivityLog:  activityLog,
		Cookies:      cookies,
		BaseURL:      strings.TrimSuffix(baseURL, "/"),
	}
}
