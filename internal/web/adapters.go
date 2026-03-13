package web

import (
	"fmt"
	"iter"

	"git.duckfam.us/jj-software/cog/internal/email"
	"git.duckfam.us/jj-software/cog/internal/group"
	internalplugin "git.duckfam.us/jj-software/cog/internal/plugin"
	"git.duckfam.us/jj-software/cog/internal/share"
	"git.duckfam.us/jj-software/cog/internal/user"
	"git.duckfam.us/jj-software/cog/internal/volume"
	"git.duckfam.us/jj-software/cog/plugin"
)

// shareStoreAdapter adapts *share.Store to plugin.ShareStore.
type shareStoreAdapter struct {
	s *share.Store
}

func (a *shareStoreAdapter) ListBySubject(subjectID string) iter.Seq2[plugin.ShareRecord, error] {
	return func(yield func(plugin.ShareRecord, error) bool) {
		for rec, err := range a.s.ListBySubject(subjectID) {
			pr := plugin.ShareRecord{
				ID:           rec.ID,
				OwnerUserID:  rec.OwnerUserID,
				ResourceType: rec.ResourceType,
				ResourceID:   rec.ResourceID,
				SubjectType:  rec.SubjectType,
				SubjectID:    rec.SubjectID,
				Permissions:  rec.Permissions,
			}
			if !yield(pr, err) {
				return
			}
		}
	}
}

func (a *shareStoreAdapter) CheckAccess(userID, resourceType, resourceID string) (*plugin.AccessDecision, error) {
	dec, err := a.s.CheckAccess(userID, resourceType, resourceID)
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

func (a *shareStoreAdapter) Create(ownerUserID, resourceType, resourceID, subjectType, subjectID string, permissions uint32) (*plugin.ShareRecord, error) {
	rec, err := a.s.Create(ownerUserID, resourceType, resourceID, subjectType, subjectID, permissions)
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

func (a *shareStoreAdapter) Delete(id string) error {
	return a.s.Delete(id)
}

// emailSenderAdapter adapts a dynamic email.Sender provider to plugin.EmailSender.
type emailSenderAdapter struct {
	senderFunc func() *email.Sender
}

func (a *emailSenderAdapter) Send(to []string, subject, body string, attachments ...plugin.EmailAttachment) error {
	s := a.senderFunc()
	if s == nil {
		return fmt.Errorf("SMTP not configured")
	}
	internal := make([]email.Attachment, len(attachments))
	for i, att := range attachments {
		internal[i] = email.Attachment{
			Filename:    att.Filename,
			ContentType: att.ContentType,
			Data:        att.Data,
		}
	}
	return s.Send(to, subject, body, internal...)
}

func (a *emailSenderAdapter) SendWithAttachments(to, subject, textBody, htmlBody string, attachments []plugin.EmailAttachment) error {
	// Delegate to Send for plain text; htmlBody is ignored since the internal sender doesn't support it.
	return a.Send([]string{to}, subject, textBody, attachments...)
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
		if parsed, err := volume.ParseURI(v.URI); err == nil {
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

// buildRegistryServices constructs the adapter layer from internal services.
func buildRegistryServices(shares *share.Store, emailFunc func() *email.Sender, users *user.Store, groups *group.Store, qc internalplugin.QuotaChecker, access *volume.Access) internalplugin.RegistryServices {
	var ss plugin.ShareStore
	if shares != nil {
		ss = &shareStoreAdapter{shares}
	}
	var es plugin.EmailSender
	if emailFunc != nil {
		es = &emailSenderAdapter{emailFunc}
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
	}
}
