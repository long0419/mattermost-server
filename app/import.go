// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See License.txt for license information.

package app

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/mattermost/mattermost-server/mlog"
	"github.com/mattermost/mattermost-server/model"
	"github.com/mattermost/mattermost-server/store"
)

// Import Data Models

type LineImportData struct {
	Type          string                   `json:"type"`
	Scheme        *SchemeImportData        `json:"scheme"`
	Team          *TeamImportData          `json:"team"`
	Channel       *ChannelImportData       `json:"channel"`
	User          *UserImportData          `json:"user"`
	Post          *PostImportData          `json:"post"`
	DirectChannel *DirectChannelImportData `json:"direct_channel"`
	DirectPost    *DirectPostImportData    `json:"direct_post"`
	Emoji         *EmojiImportData         `json:"emoji"`
	Version       *int                     `json:"version"`
}

type TeamImportData struct {
	Name            *string `json:"name"`
	DisplayName     *string `json:"display_name"`
	Type            *string `json:"type"`
	Description     *string `json:"description"`
	AllowOpenInvite *bool   `json:"allow_open_invite"`
	Scheme          *string `json:"scheme"`
}

type ChannelImportData struct {
	Team        *string `json:"team"`
	Name        *string `json:"name"`
	DisplayName *string `json:"display_name"`
	Type        *string `json:"type"`
	Header      *string `json:"header"`
	Purpose     *string `json:"purpose"`
	Scheme      *string `json:"scheme"`
}

type UserImportData struct {
	ProfileImage *string `json:"profile_image"`
	Username     *string `json:"username"`
	Email        *string `json:"email"`
	AuthService  *string `json:"auth_service"`
	AuthData     *string `json:"auth_data"`
	Password     *string `json:"password"`
	Nickname     *string `json:"nickname"`
	FirstName    *string `json:"first_name"`
	LastName     *string `json:"last_name"`
	Position     *string `json:"position"`
	Roles        *string `json:"roles"`
	Locale       *string `json:"locale"`

	Teams *[]UserTeamImportData `json:"teams"`

	Theme              *string `json:"theme"`
	UseMilitaryTime    *string `json:"military_time"`
	CollapsePreviews   *string `json:"link_previews"`
	MessageDisplay     *string `json:"message_display"`
	ChannelDisplayMode *string `json:"channel_display_mode"`
	TutorialStep       *string `json:"tutorial_step"`

	NotifyProps *UserNotifyPropsImportData `json:"notify_props"`
}

type UserNotifyPropsImportData struct {
	Desktop      *string `json:"desktop"`
	DesktopSound *string `json:"desktop_sound"`

	Email *string `json:"email"`

	Mobile           *string `json:"mobile"`
	MobilePushStatus *string `json:"mobile_push_status"`

	ChannelTrigger  *string `json:"channel"`
	CommentsTrigger *string `json:"comments"`
	MentionKeys     *string `json:"mention_keys"`
}

type UserTeamImportData struct {
	Name     *string                  `json:"name"`
	Roles    *string                  `json:"roles"`
	Channels *[]UserChannelImportData `json:"channels"`
}

type UserChannelImportData struct {
	Name        *string                           `json:"name"`
	Roles       *string                           `json:"roles"`
	NotifyProps *UserChannelNotifyPropsImportData `json:"notify_props"`
	Favorite    *bool                             `json:"favorite"`
}

type UserChannelNotifyPropsImportData struct {
	Desktop    *string `json:"desktop"`
	Mobile     *string `json:"mobile"`
	MarkUnread *string `json:"mark_unread"`
}

type EmojiImportData struct {
	Name  *string `json:"name"`
	Image *string `json:"image"`
}

type ReactionImportData struct {
	User      *string `json:"user"`
	CreateAt  *int64  `json:"create_at"`
	EmojiName *string `json:"emoji_name"`
}

type ReplyImportData struct {
	User *string `json:"user"`

	Message  *string `json:"message"`
	CreateAt *int64  `json:"create_at"`

	FlaggedBy *[]string             `json:"flagged_by"`
	Reactions *[]ReactionImportData `json:"reactions"`
}

type PostImportData struct {
	Team    *string `json:"team"`
	Channel *string `json:"channel"`
	User    *string `json:"user"`

	Message  *string `json:"message"`
	CreateAt *int64  `json:"create_at"`

	FlaggedBy *[]string             `json:"flagged_by"`
	Reactions *[]ReactionImportData `json:"reactions"`
	Replies   *[]ReplyImportData    `json:"replies"`
}

type DirectChannelImportData struct {
	Members     *[]string `json:"members"`
	FavoritedBy *[]string `json:"favorited_by"`

	Header *string `json:"header"`
}

type DirectPostImportData struct {
	ChannelMembers *[]string `json:"channel_members"`
	User           *string   `json:"user"`

	Message  *string `json:"message"`
	CreateAt *int64  `json:"create_at"`

	FlaggedBy *[]string             `json:"flagged_by"`
	Reactions *[]ReactionImportData `json:"reactions"`
	Replies   *[]ReplyImportData    `json:"replies"`
}

type SchemeImportData struct {
	Name                    *string         `json:"name"`
	DisplayName             *string         `json:"display_name"`
	Description             *string         `json:"description"`
	Scope                   *string         `json:"scope"`
	DefaultTeamAdminRole    *RoleImportData `json:"default_team_admin_role"`
	DefaultTeamUserRole     *RoleImportData `json:"default_team_user_role"`
	DefaultChannelAdminRole *RoleImportData `json:"default_channel_admin_role"`
	DefaultChannelUserRole  *RoleImportData `json:"default_channel_user_role"`
}

type RoleImportData struct {
	Name        *string   `json:"name"`
	DisplayName *string   `json:"display_name"`
	Description *string   `json:"description"`
	Permissions *[]string `json:"permissions"`
}

type LineImportWorkerData struct {
	LineImportData
	LineNumber int
}

type LineImportWorkerError struct {
	Error      *model.AppError
	LineNumber int
}

//
// -- Bulk Import Functions --
// These functions import data directly into the database. Security and permission checks are bypassed but validity is
// still enforced.
//

func (a *App) bulkImportWorker(dryRun bool, wg *sync.WaitGroup, lines <-chan LineImportWorkerData, errors chan<- LineImportWorkerError) {
	for line := range lines {
		if err := a.ImportLine(line.LineImportData, dryRun); err != nil {
			errors <- LineImportWorkerError{err, line.LineNumber}
		}
	}
	wg.Done()
}

func (a *App) BulkImport(fileReader io.Reader, dryRun bool, workers int) (*model.AppError, int) {
	scanner := bufio.NewScanner(fileReader)
	lineNumber := 0

	a.Srv.Store.LockToMaster()
	defer a.Srv.Store.UnlockFromMaster()

	errorsChan := make(chan LineImportWorkerError, (2*workers)+1) // size chosen to ensure it never gets filled up completely.
	var wg sync.WaitGroup
	var linesChan chan LineImportWorkerData
	lastLineType := ""

	for scanner.Scan() {
		decoder := json.NewDecoder(strings.NewReader(scanner.Text()))
		lineNumber++

		var line LineImportData
		if err := decoder.Decode(&line); err != nil {
			return model.NewAppError("BulkImport", "app.import.bulk_import.json_decode.error", nil, err.Error(), http.StatusBadRequest), lineNumber
		} else {
			if lineNumber == 1 {
				importDataFileVersion, apperr := processImportDataFileVersionLine(line)
				if apperr != nil {
					return apperr, lineNumber
				}

				if importDataFileVersion != 1 {
					return model.NewAppError("BulkImport", "app.import.bulk_import.unsupported_version.error", nil, "", http.StatusBadRequest), lineNumber
				}
			} else {
				if line.Type != lastLineType {
					if lastLineType != "" {
						// Changing type. Clear out the worker queue before continuing.
						close(linesChan)
						wg.Wait()

						// Check no errors occurred while waiting for the queue to empty.
						if len(errorsChan) != 0 {
							err := <-errorsChan
							return err.Error, err.LineNumber
						}
					}

					// Set up the workers and channel for this type.
					lastLineType = line.Type
					linesChan = make(chan LineImportWorkerData, workers)
					for i := 0; i < workers; i++ {
						wg.Add(1)
						go a.bulkImportWorker(dryRun, &wg, linesChan, errorsChan)
					}
				}

				select {
				case linesChan <- LineImportWorkerData{line, lineNumber}:
				case err := <-errorsChan:
					close(linesChan)
					wg.Wait()
					return err.Error, err.LineNumber
				}
			}
		}
	}

	// No more lines. Clear out the worker queue before continuing.
	close(linesChan)
	wg.Wait()

	// Check no errors occurred while waiting for the queue to empty.
	if len(errorsChan) != 0 {
		err := <-errorsChan
		return err.Error, err.LineNumber
	}

	if err := scanner.Err(); err != nil {
		return model.NewAppError("BulkImport", "app.import.bulk_import.file_scan.error", nil, err.Error(), http.StatusInternalServerError), 0
	}

	return nil, 0
}

func processImportDataFileVersionLine(line LineImportData) (int, *model.AppError) {
	if line.Type != "version" || line.Version == nil {
		return -1, model.NewAppError("BulkImport", "app.import.process_import_data_file_version_line.invalid_version.error", nil, "", http.StatusBadRequest)
	}

	return *line.Version, nil
}

func (a *App) ImportLine(line LineImportData, dryRun bool) *model.AppError {
	switch {
	case line.Type == "scheme":
		if line.Scheme == nil {
			return model.NewAppError("BulkImport", "app.import.import_line.null_scheme.error", nil, "", http.StatusBadRequest)
		} else {
			return a.ImportScheme(line.Scheme, dryRun)
		}
	case line.Type == "team":
		if line.Team == nil {
			return model.NewAppError("BulkImport", "app.import.import_line.null_team.error", nil, "", http.StatusBadRequest)
		} else {
			return a.ImportTeam(line.Team, dryRun)
		}
	case line.Type == "channel":
		if line.Channel == nil {
			return model.NewAppError("BulkImport", "app.import.import_line.null_channel.error", nil, "", http.StatusBadRequest)
		} else {
			return a.ImportChannel(line.Channel, dryRun)
		}
	case line.Type == "user":
		if line.User == nil {
			return model.NewAppError("BulkImport", "app.import.import_line.null_user.error", nil, "", http.StatusBadRequest)
		} else {
			return a.ImportUser(line.User, dryRun)
		}
	case line.Type == "post":
		if line.Post == nil {
			return model.NewAppError("BulkImport", "app.import.import_line.null_post.error", nil, "", http.StatusBadRequest)
		} else {
			return a.ImportPost(line.Post, dryRun)
		}
	case line.Type == "direct_channel":
		if line.DirectChannel == nil {
			return model.NewAppError("BulkImport", "app.import.import_line.null_direct_channel.error", nil, "", http.StatusBadRequest)
		} else {
			return a.ImportDirectChannel(line.DirectChannel, dryRun)
		}
	case line.Type == "direct_post":
		if line.DirectPost == nil {
			return model.NewAppError("BulkImport", "app.import.import_line.null_direct_post.error", nil, "", http.StatusBadRequest)
		} else {
			return a.ImportDirectPost(line.DirectPost, dryRun)
		}
	case line.Type == "emoji":
		if line.Emoji == nil {
			return model.NewAppError("BulkImport", "app.import.import_line.null_emoji.error", nil, "", http.StatusBadRequest)
		} else {
			return a.ImportEmoji(line.Emoji, dryRun)
		}
	default:
		return model.NewAppError("BulkImport", "app.import.import_line.unknown_line_type.error", map[string]interface{}{"Type": line.Type}, "", http.StatusBadRequest)
	}
}

func (a *App) ImportScheme(data *SchemeImportData, dryRun bool) *model.AppError {
	if err := validateSchemeImportData(data); err != nil {
		return err
	}

	// If this is a Dry Run, do not continue any further.
	if dryRun {
		return nil
	}

	scheme, err := a.GetSchemeByName(*data.Name)
	if err != nil {
		scheme = new(model.Scheme)
	} else if scheme.Scope != *data.Scope {
		return model.NewAppError("BulkImport", "app.import.import_scheme.scope_change.error", map[string]interface{}{"SchemeName": scheme.Name}, "", http.StatusBadRequest)
	}

	scheme.Name = *data.Name
	scheme.DisplayName = *data.DisplayName
	scheme.Scope = *data.Scope

	if data.Description != nil {
		scheme.Description = *data.Description
	}

	if len(scheme.Id) == 0 {
		scheme, err = a.CreateScheme(scheme)
	} else {
		scheme, err = a.UpdateScheme(scheme)
	}

	if err != nil {
		return err
	}

	if scheme.Scope == model.SCHEME_SCOPE_TEAM {
		data.DefaultTeamAdminRole.Name = &scheme.DefaultTeamAdminRole
		if err := a.ImportRole(data.DefaultTeamAdminRole, dryRun, true); err != nil {
			return err
		}

		data.DefaultTeamUserRole.Name = &scheme.DefaultTeamUserRole
		if err := a.ImportRole(data.DefaultTeamUserRole, dryRun, true); err != nil {
			return err
		}
	}

	if scheme.Scope == model.SCHEME_SCOPE_TEAM || scheme.Scope == model.SCHEME_SCOPE_CHANNEL {
		data.DefaultChannelAdminRole.Name = &scheme.DefaultChannelAdminRole
		if err := a.ImportRole(data.DefaultChannelAdminRole, dryRun, true); err != nil {
			return err
		}

		data.DefaultChannelUserRole.Name = &scheme.DefaultChannelUserRole
		if err := a.ImportRole(data.DefaultChannelUserRole, dryRun, true); err != nil {
			return err
		}
	}

	return nil
}

func (a *App) ImportRole(data *RoleImportData, dryRun bool, isSchemeRole bool) *model.AppError {
	if !isSchemeRole {
		if err := validateRoleImportData(data); err != nil {
			return err
		}
	}

	// If this is a Dry Run, do not continue any further.
	if dryRun {
		return nil
	}

	role, err := a.GetRoleByName(*data.Name)
	if err != nil {
		role = new(model.Role)
	}

	role.Name = *data.Name

	if data.DisplayName != nil {
		role.DisplayName = *data.DisplayName
	}

	if data.Description != nil {
		role.Description = *data.Description
	}

	if data.Permissions != nil {
		role.Permissions = *data.Permissions
	}

	if isSchemeRole {
		role.SchemeManaged = true
	} else {
		role.SchemeManaged = false
	}

	if len(role.Id) == 0 {
		role, err = a.CreateRole(role)
	} else {
		role, err = a.UpdateRole(role)
	}

	return err
}

func validateSchemeImportData(data *SchemeImportData) *model.AppError {

	if data.Scope == nil {
		return model.NewAppError("BulkImport", "app.import.validate_scheme_import_data.null_scope.error", nil, "", http.StatusBadRequest)
	}

	switch *data.Scope {
	case model.SCHEME_SCOPE_TEAM:
		if data.DefaultTeamAdminRole == nil || data.DefaultTeamUserRole == nil || data.DefaultChannelAdminRole == nil || data.DefaultChannelUserRole == nil {
			return model.NewAppError("BulkImport", "app.import.validate_scheme_import_data.wrong_roles_for_scope.error", nil, "", http.StatusBadRequest)
		}
	case model.SCHEME_SCOPE_CHANNEL:
		if data.DefaultTeamAdminRole != nil || data.DefaultTeamUserRole != nil || data.DefaultChannelAdminRole == nil || data.DefaultChannelUserRole == nil {
			return model.NewAppError("BulkImport", "app.import.validate_scheme_import_data.wrong_roles_for_scope.error", nil, "", http.StatusBadRequest)
		}
	default:
		return model.NewAppError("BulkImport", "app.import.validate_scheme_import_data.unknown_scheme.error", nil, "", http.StatusBadRequest)
	}

	if data.Name == nil || !model.IsValidSchemeName(*data.Name) {
		return model.NewAppError("BulkImport", "app.import.validate_scheme_import_data.name_invalid.error", nil, "", http.StatusBadRequest)
	}

	if data.DisplayName == nil || len(*data.DisplayName) == 0 || len(*data.DisplayName) > model.SCHEME_DISPLAY_NAME_MAX_LENGTH {
		return model.NewAppError("BulkImport", "app.import.validate_scheme_import_data.display_name_invalid.error", nil, "", http.StatusBadRequest)
	}

	if data.Description != nil && len(*data.Description) > model.SCHEME_DESCRIPTION_MAX_LENGTH {
		return model.NewAppError("BulkImport", "app.import.validate_scheme_import_data.description_invalid.error", nil, "", http.StatusBadRequest)
	}

	if data.DefaultTeamAdminRole != nil {
		if err := validateRoleImportData(data.DefaultTeamAdminRole); err != nil {
			return err
		}
	}

	if data.DefaultTeamUserRole != nil {
		if err := validateRoleImportData(data.DefaultTeamUserRole); err != nil {
			return err
		}
	}

	if data.DefaultChannelAdminRole != nil {
		if err := validateRoleImportData(data.DefaultChannelAdminRole); err != nil {
			return err
		}
	}

	if data.DefaultChannelUserRole != nil {
		if err := validateRoleImportData(data.DefaultChannelUserRole); err != nil {
			return err
		}
	}

	return nil
}

func validateRoleImportData(data *RoleImportData) *model.AppError {

	if data.Name == nil || !model.IsValidRoleName(*data.Name) {
		return model.NewAppError("BulkImport", "app.import.validate_role_import_data.name_invalid.error", nil, "", http.StatusBadRequest)
	}

	if data.DisplayName == nil || len(*data.DisplayName) == 0 || len(*data.DisplayName) > model.ROLE_DISPLAY_NAME_MAX_LENGTH {
		return model.NewAppError("BulkImport", "app.import.validate_role_import_data.display_name_invalid.error", nil, "", http.StatusBadRequest)
	}

	if data.Description != nil && len(*data.Description) > model.ROLE_DESCRIPTION_MAX_LENGTH {
		return model.NewAppError("BulkImport", "app.import.validate_role_import_data.description_invalid.error", nil, "", http.StatusBadRequest)
	}

	if data.Permissions != nil {
		for _, permission := range *data.Permissions {
			permissionValidated := false
			for _, p := range model.ALL_PERMISSIONS {
				if permission == p.Id {
					permissionValidated = true
					break
				}
			}

			if !permissionValidated {
				return model.NewAppError("BulkImport", "app.import.validate_role_import_data.invalid_permission.error", nil, "permission"+permission, http.StatusBadRequest)
			}
		}
	}

	return nil
}

func (a *App) ImportTeam(data *TeamImportData, dryRun bool) *model.AppError {
	if err := validateTeamImportData(data); err != nil {
		return err
	}

	// If this is a Dry Run, do not continue any further.
	if dryRun {
		return nil
	}

	var team *model.Team
	if result := <-a.Srv.Store.Team().GetByName(*data.Name); result.Err == nil {
		team = result.Data.(*model.Team)
	} else {
		team = &model.Team{}
	}

	team.Name = *data.Name
	team.DisplayName = *data.DisplayName
	team.Type = *data.Type

	if data.Description != nil {
		team.Description = *data.Description
	}

	if data.AllowOpenInvite != nil {
		team.AllowOpenInvite = *data.AllowOpenInvite
	}

	if data.Scheme != nil {
		scheme, err := a.GetSchemeByName(*data.Scheme)
		if err != nil {
			return err
		}

		if scheme.DeleteAt != 0 {
			return model.NewAppError("BulkImport", "app.import.import_team.scheme_deleted.error", nil, "", http.StatusBadRequest)
		}

		if scheme.Scope != model.SCHEME_SCOPE_TEAM {
			return model.NewAppError("BulkImport", "app.import.import_team.scheme_wrong_scope.error", nil, "", http.StatusBadRequest)
		}

		team.SchemeId = &scheme.Id
	}

	if team.Id == "" {
		if _, err := a.CreateTeam(team); err != nil {
			return err
		}
	} else {
		if _, err := a.updateTeamUnsanitized(team); err != nil {
			return err
		}
	}

	return nil
}

func validateTeamImportData(data *TeamImportData) *model.AppError {

	if data.Name == nil {
		return model.NewAppError("BulkImport", "app.import.validate_team_import_data.name_missing.error", nil, "", http.StatusBadRequest)
	} else if len(*data.Name) > model.TEAM_NAME_MAX_LENGTH {
		return model.NewAppError("BulkImport", "app.import.validate_team_import_data.name_length.error", nil, "", http.StatusBadRequest)
	} else if model.IsReservedTeamName(*data.Name) {
		return model.NewAppError("BulkImport", "app.import.validate_team_import_data.name_reserved.error", nil, "", http.StatusBadRequest)
	} else if !model.IsValidTeamName(*data.Name) {
		return model.NewAppError("BulkImport", "app.import.validate_team_import_data.name_characters.error", nil, "", http.StatusBadRequest)
	}

	if data.DisplayName == nil {
		return model.NewAppError("BulkImport", "app.import.validate_team_import_data.display_name_missing.error", nil, "", http.StatusBadRequest)
	} else if utf8.RuneCountInString(*data.DisplayName) == 0 || utf8.RuneCountInString(*data.DisplayName) > model.TEAM_DISPLAY_NAME_MAX_RUNES {
		return model.NewAppError("BulkImport", "app.import.validate_team_import_data.display_name_length.error", nil, "", http.StatusBadRequest)
	}

	if data.Type == nil {
		return model.NewAppError("BulkImport", "app.import.validate_team_import_data.type_missing.error", nil, "", http.StatusBadRequest)
	} else if *data.Type != model.TEAM_OPEN && *data.Type != model.TEAM_INVITE {
		return model.NewAppError("BulkImport", "app.import.validate_team_import_data.type_invalid.error", nil, "", http.StatusBadRequest)
	}

	if data.Description != nil && len(*data.Description) > model.TEAM_DESCRIPTION_MAX_LENGTH {
		return model.NewAppError("BulkImport", "app.import.validate_team_import_data.description_length.error", nil, "", http.StatusBadRequest)
	}

	if data.Scheme != nil && !model.IsValidSchemeName(*data.Scheme) {
		return model.NewAppError("BulkImport", "app.import.validate_team_import_data.scheme_invalid.error", nil, "", http.StatusBadRequest)
	}

	return nil
}

func (a *App) ImportChannel(data *ChannelImportData, dryRun bool) *model.AppError {
	if err := validateChannelImportData(data); err != nil {
		return err
	}

	// If this is a Dry Run, do not continue any further.
	if dryRun {
		return nil
	}

	var team *model.Team
	if result := <-a.Srv.Store.Team().GetByName(*data.Team); result.Err != nil {
		return model.NewAppError("BulkImport", "app.import.import_channel.team_not_found.error", map[string]interface{}{"TeamName": *data.Team}, result.Err.Error(), http.StatusBadRequest)
	} else {
		team = result.Data.(*model.Team)
	}

	var channel *model.Channel
	if result := <-a.Srv.Store.Channel().GetByNameIncludeDeleted(team.Id, *data.Name, true); result.Err == nil {
		channel = result.Data.(*model.Channel)
	} else {
		channel = &model.Channel{}
	}

	channel.TeamId = team.Id
	channel.Name = *data.Name
	channel.DisplayName = *data.DisplayName
	channel.Type = *data.Type

	if data.Header != nil {
		channel.Header = *data.Header
	}

	if data.Purpose != nil {
		channel.Purpose = *data.Purpose
	}

	if data.Scheme != nil {
		scheme, err := a.GetSchemeByName(*data.Scheme)
		if err != nil {
			return err
		}

		if scheme.DeleteAt != 0 {
			return model.NewAppError("BulkImport", "app.import.import_channel.scheme_deleted.error", nil, "", http.StatusBadRequest)
		}

		if scheme.Scope != model.SCHEME_SCOPE_CHANNEL {
			return model.NewAppError("BulkImport", "app.import.import_channel.scheme_wrong_scope.error", nil, "", http.StatusBadRequest)
		}

		channel.SchemeId = &scheme.Id
	}

	if channel.Id == "" {
		if _, err := a.CreateChannel(channel, false); err != nil {
			return err
		}
	} else {
		if _, err := a.UpdateChannel(channel); err != nil {
			return err
		}
	}

	return nil
}

func validateChannelImportData(data *ChannelImportData) *model.AppError {

	if data.Team == nil {
		return model.NewAppError("BulkImport", "app.import.validate_channel_import_data.team_missing.error", nil, "", http.StatusBadRequest)
	}

	if data.Name == nil {
		return model.NewAppError("BulkImport", "app.import.validate_channel_import_data.name_missing.error", nil, "", http.StatusBadRequest)
	} else if len(*data.Name) > model.CHANNEL_NAME_MAX_LENGTH {
		return model.NewAppError("BulkImport", "app.import.validate_channel_import_data.name_length.error", nil, "", http.StatusBadRequest)
	} else if !model.IsValidChannelIdentifier(*data.Name) {
		return model.NewAppError("BulkImport", "app.import.validate_channel_import_data.name_characters.error", nil, "", http.StatusBadRequest)
	}

	if data.DisplayName == nil {
		return model.NewAppError("BulkImport", "app.import.validate_channel_import_data.display_name_missing.error", nil, "", http.StatusBadRequest)
	} else if utf8.RuneCountInString(*data.DisplayName) == 0 || utf8.RuneCountInString(*data.DisplayName) > model.CHANNEL_DISPLAY_NAME_MAX_RUNES {
		return model.NewAppError("BulkImport", "app.import.validate_channel_import_data.display_name_length.error", nil, "", http.StatusBadRequest)
	}

	if data.Type == nil {
		return model.NewAppError("BulkImport", "app.import.validate_channel_import_data.type_missing.error", nil, "", http.StatusBadRequest)
	} else if *data.Type != model.CHANNEL_OPEN && *data.Type != model.CHANNEL_PRIVATE {
		return model.NewAppError("BulkImport", "app.import.validate_channel_import_data.type_invalid.error", nil, "", http.StatusBadRequest)
	}

	if data.Header != nil && utf8.RuneCountInString(*data.Header) > model.CHANNEL_HEADER_MAX_RUNES {
		return model.NewAppError("BulkImport", "app.import.validate_channel_import_data.header_length.error", nil, "", http.StatusBadRequest)
	}

	if data.Purpose != nil && utf8.RuneCountInString(*data.Purpose) > model.CHANNEL_PURPOSE_MAX_RUNES {
		return model.NewAppError("BulkImport", "app.import.validate_channel_import_data.purpose_length.error", nil, "", http.StatusBadRequest)
	}

	if data.Scheme != nil && !model.IsValidSchemeName(*data.Scheme) {
		return model.NewAppError("BulkImport", "app.import.validate_channel_import_data.scheme_invalid.error", nil, "", http.StatusBadRequest)
	}

	return nil
}

func (a *App) ImportUser(data *UserImportData, dryRun bool) *model.AppError {
	if err := validateUserImportData(data); err != nil {
		return err
	}

	// If this is a Dry Run, do not continue any further.
	if dryRun {
		return nil
	}

	// We want to avoid database writes if nothing has changed.
	hasUserChanged := false
	hasNotifyPropsChanged := false
	hasUserRolesChanged := false
	hasUserAuthDataChanged := false
	hasUserEmailVerifiedChanged := false

	var user *model.User
	if result := <-a.Srv.Store.User().GetByUsername(*data.Username); result.Err == nil {
		user = result.Data.(*model.User)
	} else {
		user = &model.User{}
		user.MakeNonNil()
		hasUserChanged = true
	}

	user.Username = *data.Username

	if user.Email != *data.Email {
		hasUserChanged = true
		hasUserEmailVerifiedChanged = true // Changing the email resets email verified to false by default.
		user.Email = *data.Email
	}

	var password string
	var authService string
	var authData *string

	if data.AuthService != nil {
		if user.AuthService != *data.AuthService {
			hasUserAuthDataChanged = true
		}
		authService = *data.AuthService
	}

	// AuthData and Password are mutually exclusive.
	if data.AuthData != nil {
		if user.AuthData == nil || *user.AuthData != *data.AuthData {
			hasUserAuthDataChanged = true
		}
		authData = data.AuthData
		password = ""
	} else if data.Password != nil {
		password = *data.Password
		authData = nil
	} else {
		// If no AuthData or Password is specified, we must generate a password.
		password = model.NewId()
		authData = nil
	}

	user.Password = password
	user.AuthService = authService
	user.AuthData = authData

	// Automatically assume all emails are verified.
	emailVerified := true
	if user.EmailVerified != emailVerified {
		user.EmailVerified = emailVerified
		hasUserEmailVerifiedChanged = true
	}

	if data.Nickname != nil {
		if user.Nickname != *data.Nickname {
			user.Nickname = *data.Nickname
			hasUserChanged = true
		}
	}

	if data.FirstName != nil {
		if user.FirstName != *data.FirstName {
			user.FirstName = *data.FirstName
			hasUserChanged = true
		}
	}

	if data.LastName != nil {
		if user.LastName != *data.LastName {
			user.LastName = *data.LastName
			hasUserChanged = true
		}
	}

	if data.Position != nil {
		if user.Position != *data.Position {
			user.Position = *data.Position
			hasUserChanged = true
		}
	}

	if data.Locale != nil {
		if user.Locale != *data.Locale {
			user.Locale = *data.Locale
			hasUserChanged = true
		}
	} else {
		if user.Locale != *a.Config().LocalizationSettings.DefaultClientLocale {
			user.Locale = *a.Config().LocalizationSettings.DefaultClientLocale
			hasUserChanged = true
		}
	}

	var roles string
	if data.Roles != nil {
		if user.Roles != *data.Roles {
			roles = *data.Roles
			hasUserRolesChanged = true
		}
	} else if len(user.Roles) == 0 {
		// Set SYSTEM_USER roles on newly created users by default.
		if user.Roles != model.SYSTEM_USER_ROLE_ID {
			roles = model.SYSTEM_USER_ROLE_ID
			hasUserRolesChanged = true
		}
	}
	user.Roles = roles

	if data.NotifyProps != nil {
		if data.NotifyProps.Desktop != nil {
			if value, ok := user.NotifyProps[model.DESKTOP_NOTIFY_PROP]; !ok || value != *data.NotifyProps.Desktop {
				user.AddNotifyProp(model.DESKTOP_NOTIFY_PROP, *data.NotifyProps.Desktop)
				hasNotifyPropsChanged = true
			}
		}

		if data.NotifyProps.DesktopSound != nil {
			if value, ok := user.NotifyProps[model.DESKTOP_SOUND_NOTIFY_PROP]; !ok || value != *data.NotifyProps.DesktopSound {
				user.AddNotifyProp(model.DESKTOP_SOUND_NOTIFY_PROP, *data.NotifyProps.DesktopSound)
				hasNotifyPropsChanged = true
			}
		}

		if data.NotifyProps.Email != nil {
			if value, ok := user.NotifyProps[model.EMAIL_NOTIFY_PROP]; !ok || value != *data.NotifyProps.Email {
				user.AddNotifyProp(model.EMAIL_NOTIFY_PROP, *data.NotifyProps.Email)
				hasNotifyPropsChanged = true
			}
		}

		if data.NotifyProps.Mobile != nil {
			if value, ok := user.NotifyProps[model.PUSH_NOTIFY_PROP]; !ok || value != *data.NotifyProps.Mobile {
				user.AddNotifyProp(model.PUSH_NOTIFY_PROP, *data.NotifyProps.Mobile)
				hasNotifyPropsChanged = true
			}
		}

		if data.NotifyProps.MobilePushStatus != nil {
			if value, ok := user.NotifyProps[model.PUSH_STATUS_NOTIFY_PROP]; !ok || value != *data.NotifyProps.MobilePushStatus {
				user.AddNotifyProp(model.PUSH_STATUS_NOTIFY_PROP, *data.NotifyProps.MobilePushStatus)
				hasNotifyPropsChanged = true
			}
		}

		if data.NotifyProps.ChannelTrigger != nil {
			if value, ok := user.NotifyProps[model.CHANNEL_MENTIONS_NOTIFY_PROP]; !ok || value != *data.NotifyProps.ChannelTrigger {
				user.AddNotifyProp(model.CHANNEL_MENTIONS_NOTIFY_PROP, *data.NotifyProps.ChannelTrigger)
				hasNotifyPropsChanged = true
			}
		}

		if data.NotifyProps.CommentsTrigger != nil {
			if value, ok := user.NotifyProps[model.COMMENTS_NOTIFY_PROP]; !ok || value != *data.NotifyProps.CommentsTrigger {
				user.AddNotifyProp(model.COMMENTS_NOTIFY_PROP, *data.NotifyProps.CommentsTrigger)
				hasNotifyPropsChanged = true
			}
		}

		if data.NotifyProps.MentionKeys != nil {
			if value, ok := user.NotifyProps[model.MENTION_KEYS_NOTIFY_PROP]; !ok || value != *data.NotifyProps.MentionKeys {
				user.AddNotifyProp(model.MENTION_KEYS_NOTIFY_PROP, *data.NotifyProps.MentionKeys)
				hasNotifyPropsChanged = true
			}
		}
	}

	var err *model.AppError
	var savedUser *model.User
	if user.Id == "" {
		if savedUser, err = a.createUser(user); err != nil {
			return err
		}
	} else {
		if hasUserChanged {
			if savedUser, err = a.UpdateUser(user, false); err != nil {
				return err
			}
		}
		if hasUserRolesChanged {
			if savedUser, err = a.UpdateUserRoles(user.Id, roles, false); err != nil {
				return err
			}
		}
		if hasNotifyPropsChanged {
			if savedUser, err = a.UpdateUserNotifyProps(user.Id, user.NotifyProps); err != nil {
				return err
			}
		}
		if len(password) > 0 {
			if err = a.UpdatePassword(user, password); err != nil {
				return err
			}
		} else {
			if hasUserAuthDataChanged {
				if res := <-a.Srv.Store.User().UpdateAuthData(user.Id, authService, authData, user.Email, false); res.Err != nil {
					return res.Err
				}
			}
		}
		if emailVerified {
			if hasUserEmailVerifiedChanged {
				if err := a.VerifyUserEmail(user.Id); err != nil {
					return err
				}
			}
		}
	}

	if savedUser == nil {
		savedUser = user
	}

	if data.ProfileImage != nil {
		file, err := os.Open(*data.ProfileImage)
		if err != nil {
			mlog.Error(fmt.Sprint("api.import.import_user.profile_image.error FIXME: NOT FOUND IN TRANSLATIONS FILE", err))
		}
		if err := a.SetProfileImageFromFile(savedUser.Id, file); err != nil {
			mlog.Error(fmt.Sprint("api.import.import_user.profile_image.error FIXME: NOT FOUND IN TRANSLATIONS FILE", err))
		}
	}

	// Preferences.
	var preferences model.Preferences

	if data.Theme != nil {
		preferences = append(preferences, model.Preference{
			UserId:   savedUser.Id,
			Category: model.PREFERENCE_CATEGORY_THEME,
			Name:     "",
			Value:    *data.Theme,
		})
	}

	if data.UseMilitaryTime != nil {
		preferences = append(preferences, model.Preference{
			UserId:   savedUser.Id,
			Category: model.PREFERENCE_CATEGORY_DISPLAY_SETTINGS,
			Name:     "use_military_time",
			Value:    *data.UseMilitaryTime,
		})
	}

	if data.CollapsePreviews != nil {
		preferences = append(preferences, model.Preference{
			UserId:   savedUser.Id,
			Category: model.PREFERENCE_CATEGORY_DISPLAY_SETTINGS,
			Name:     "collapse_previews",
			Value:    *data.CollapsePreviews,
		})
	}

	if data.MessageDisplay != nil {
		preferences = append(preferences, model.Preference{
			UserId:   savedUser.Id,
			Category: model.PREFERENCE_CATEGORY_DISPLAY_SETTINGS,
			Name:     "message_display",
			Value:    *data.MessageDisplay,
		})
	}

	if data.ChannelDisplayMode != nil {
		preferences = append(preferences, model.Preference{
			UserId:   savedUser.Id,
			Category: model.PREFERENCE_CATEGORY_DISPLAY_SETTINGS,
			Name:     "channel_display_mode",
			Value:    *data.ChannelDisplayMode,
		})
	}

	if data.TutorialStep != nil {
		preferences = append(preferences, model.Preference{
			UserId:   savedUser.Id,
			Category: model.PREFERENCE_CATEGORY_TUTORIAL_STEPS,
			Name:     savedUser.Id,
			Value:    *data.TutorialStep,
		})
	}

	if len(preferences) > 0 {
		if result := <-a.Srv.Store.Preference().Save(&preferences); result.Err != nil {
			return model.NewAppError("BulkImport", "app.import.import_user.save_preferences.error", nil, result.Err.Error(), http.StatusInternalServerError)
		}
	}

	return a.ImportUserTeams(savedUser, data.Teams)
}

func (a *App) ImportUserTeams(user *model.User, data *[]UserTeamImportData) *model.AppError {
	if data == nil {
		return nil
	}

	for _, tdata := range *data {
		team, err := a.GetTeamByName(*tdata.Name)
		if err != nil {
			return err
		}

		var roles string
		isSchemeUser := true
		isSchemeAdmin := false

		if tdata.Roles == nil {
			isSchemeUser = true
		} else {
			rawRoles := *tdata.Roles
			explicitRoles := []string{}
			for _, role := range strings.Fields(rawRoles) {
				if role == model.TEAM_USER_ROLE_ID {
					isSchemeUser = true
				} else if role == model.TEAM_ADMIN_ROLE_ID {
					isSchemeAdmin = true
				} else {
					explicitRoles = append(explicitRoles, role)
				}
			}
			roles = strings.Join(explicitRoles, " ")
		}

		var member *model.TeamMember
		if member, _, err = a.joinUserToTeam(team, user); err != nil {
			return err
		}

		if member.ExplicitRoles != roles {
			if _, err := a.UpdateTeamMemberRoles(team.Id, user.Id, roles); err != nil {
				return err
			}
		}

		if member.SchemeAdmin != isSchemeAdmin || member.SchemeUser != isSchemeUser {
			a.UpdateTeamMemberSchemeRoles(team.Id, user.Id, isSchemeUser, isSchemeAdmin)
		}

		if defaultChannel, err := a.GetChannelByName(model.DEFAULT_CHANNEL, team.Id); err != nil {
			return err
		} else if _, err = a.addUserToChannel(user, defaultChannel, member); err != nil {
			return err
		}

		if err := a.ImportUserChannels(user, team, member, tdata.Channels); err != nil {
			return err
		}
	}

	return nil
}

func (a *App) ImportUserChannels(user *model.User, team *model.Team, teamMember *model.TeamMember, data *[]UserChannelImportData) *model.AppError {
	if data == nil {
		return nil
	}

	var preferences model.Preferences

	// Loop through all channels.
	for _, cdata := range *data {
		channel, err := a.GetChannelByName(*cdata.Name, team.Id)
		if err != nil {
			return err
		}

		var roles string
		isSchemeUser := true
		isSchemeAdmin := false

		if cdata.Roles == nil {
			isSchemeUser = true
		} else {
			rawRoles := *cdata.Roles
			explicitRoles := []string{}
			for _, role := range strings.Fields(rawRoles) {
				if role == model.CHANNEL_USER_ROLE_ID {
					isSchemeUser = true
				} else if role == model.CHANNEL_ADMIN_ROLE_ID {
					isSchemeAdmin = true
				} else {
					explicitRoles = append(explicitRoles, role)
				}
			}
			roles = strings.Join(explicitRoles, " ")
		}

		var member *model.ChannelMember
		member, err = a.GetChannelMember(channel.Id, user.Id)
		if err != nil {
			member, err = a.addUserToChannel(user, channel, teamMember)
			if err != nil {
				return err
			}
		}

		if member.ExplicitRoles != roles {
			if _, err := a.UpdateChannelMemberRoles(channel.Id, user.Id, roles); err != nil {
				return err
			}
		}

		if member.SchemeAdmin != isSchemeAdmin || member.SchemeUser != isSchemeUser {
			a.UpdateChannelMemberSchemeRoles(channel.Id, user.Id, isSchemeUser, isSchemeAdmin)
		}

		if cdata.NotifyProps != nil {
			notifyProps := member.NotifyProps

			if cdata.NotifyProps.Desktop != nil {
				notifyProps[model.DESKTOP_NOTIFY_PROP] = *cdata.NotifyProps.Desktop
			}

			if cdata.NotifyProps.Mobile != nil {
				notifyProps[model.PUSH_NOTIFY_PROP] = *cdata.NotifyProps.Mobile
			}

			if cdata.NotifyProps.MarkUnread != nil {
				notifyProps[model.MARK_UNREAD_NOTIFY_PROP] = *cdata.NotifyProps.MarkUnread
			}

			if _, err := a.UpdateChannelMemberNotifyProps(notifyProps, channel.Id, user.Id); err != nil {
				return err
			}
		}

		if cdata.Favorite != nil && *cdata.Favorite {
			preferences = append(preferences, model.Preference{
				UserId:   user.Id,
				Category: model.PREFERENCE_CATEGORY_FAVORITE_CHANNEL,
				Name:     channel.Id,
				Value:    "true",
			})
		}
	}

	if len(preferences) > 0 {
		if result := <-a.Srv.Store.Preference().Save(&preferences); result.Err != nil {
			return model.NewAppError("BulkImport", "app.import.import_user_channels.save_preferences.error", nil, result.Err.Error(), http.StatusInternalServerError)
		}
	}

	return nil
}

func validateUserImportData(data *UserImportData) *model.AppError {
	if data.ProfileImage != nil {
		if _, err := os.Stat(*data.ProfileImage); os.IsNotExist(err) {
			return model.NewAppError("BulkImport", "app.import.validate_user_import_data.profile_image.error", nil, "", http.StatusBadRequest)
		}
	}

	if data.Username == nil {
		return model.NewAppError("BulkImport", "app.import.validate_user_import_data.username_missing.error", nil, "", http.StatusBadRequest)
	} else if !model.IsValidUsername(*data.Username) {
		return model.NewAppError("BulkImport", "app.import.validate_user_import_data.username_invalid.error", nil, "", http.StatusBadRequest)
	}

	if data.Email == nil {
		return model.NewAppError("BulkImport", "app.import.validate_user_import_data.email_missing.error", nil, "", http.StatusBadRequest)
	} else if len(*data.Email) == 0 || len(*data.Email) > model.USER_EMAIL_MAX_LENGTH {
		return model.NewAppError("BulkImport", "app.import.validate_user_import_data.email_length.error", nil, "", http.StatusBadRequest)
	}

	if data.AuthService != nil && len(*data.AuthService) == 0 {
		return model.NewAppError("BulkImport", "app.import.validate_user_import_data.auth_service_length.error", nil, "", http.StatusBadRequest)
	}

	if data.AuthData != nil && data.Password != nil {
		return model.NewAppError("BulkImport", "app.import.validate_user_import_data.auth_data_and_password.error", nil, "", http.StatusBadRequest)
	}

	if data.AuthData != nil && len(*data.AuthData) > model.USER_AUTH_DATA_MAX_LENGTH {
		return model.NewAppError("BulkImport", "app.import.validate_user_import_data.auth_data_length.error", nil, "", http.StatusBadRequest)
	}

	if data.Password != nil && len(*data.Password) == 0 {
		return model.NewAppError("BulkImport", "app.import.validate_user_import_data.password_length.error", nil, "", http.StatusBadRequest)
	}

	if data.Password != nil && len(*data.Password) > model.USER_PASSWORD_MAX_LENGTH {
		return model.NewAppError("BulkImport", "app.import.validate_user_import_data.password_length.error", nil, "", http.StatusBadRequest)
	}

	if data.Nickname != nil && utf8.RuneCountInString(*data.Nickname) > model.USER_NICKNAME_MAX_RUNES {
		return model.NewAppError("BulkImport", "app.import.validate_user_import_data.nickname_length.error", nil, "", http.StatusBadRequest)
	}

	if data.FirstName != nil && utf8.RuneCountInString(*data.FirstName) > model.USER_FIRST_NAME_MAX_RUNES {
		return model.NewAppError("BulkImport", "app.import.validate_user_import_data.first_name_length.error", nil, "", http.StatusBadRequest)
	}

	if data.LastName != nil && utf8.RuneCountInString(*data.LastName) > model.USER_LAST_NAME_MAX_RUNES {
		return model.NewAppError("BulkImport", "app.import.validate_user_import_data.last_name_length.error", nil, "", http.StatusBadRequest)
	}

	if data.Position != nil && utf8.RuneCountInString(*data.Position) > model.USER_POSITION_MAX_RUNES {
		return model.NewAppError("BulkImport", "app.import.validate_user_import_data.position_length.error", nil, "", http.StatusBadRequest)
	}

	if data.Roles != nil && !model.IsValidUserRoles(*data.Roles) {
		return model.NewAppError("BulkImport", "app.import.validate_user_import_data.roles_invalid.error", nil, "", http.StatusBadRequest)
	}

	if data.NotifyProps != nil {
		if data.NotifyProps.Desktop != nil && !model.IsValidUserNotifyLevel(*data.NotifyProps.Desktop) {
			return model.NewAppError("BulkImport", "app.import.validate_user_import_data.notify_props_desktop_invalid.error", nil, "", http.StatusBadRequest)
		}

		if data.NotifyProps.DesktopSound != nil && !model.IsValidTrueOrFalseString(*data.NotifyProps.DesktopSound) {
			return model.NewAppError("BulkImport", "app.import.validate_user_import_data.notify_props_desktop_sound_invalid.error", nil, "", http.StatusBadRequest)
		}

		if data.NotifyProps.Email != nil && !model.IsValidTrueOrFalseString(*data.NotifyProps.Email) {
			return model.NewAppError("BulkImport", "app.import.validate_user_import_data.notify_props_email_invalid.error", nil, "", http.StatusBadRequest)
		}

		if data.NotifyProps.Mobile != nil && !model.IsValidUserNotifyLevel(*data.NotifyProps.Mobile) {
			return model.NewAppError("BulkImport", "app.import.validate_user_import_data.notify_props_mobile_invalid.error", nil, "", http.StatusBadRequest)
		}

		if data.NotifyProps.MobilePushStatus != nil && !model.IsValidPushStatusNotifyLevel(*data.NotifyProps.MobilePushStatus) {
			return model.NewAppError("BulkImport", "app.import.validate_user_import_data.notify_props_mobile_push_status_invalid.error", nil, "", http.StatusBadRequest)
		}

		if data.NotifyProps.ChannelTrigger != nil && !model.IsValidTrueOrFalseString(*data.NotifyProps.ChannelTrigger) {
			return model.NewAppError("BulkImport", "app.import.validate_user_import_data.notify_props_channel_trigger_invalid.error", nil, "", http.StatusBadRequest)
		}

		if data.NotifyProps.CommentsTrigger != nil && !model.IsValidCommentsNotifyLevel(*data.NotifyProps.CommentsTrigger) {
			return model.NewAppError("BulkImport", "app.import.validate_user_import_data.notify_props_comments_trigger_invalid.error", nil, "", http.StatusBadRequest)
		}
	}

	if data.Teams != nil {
		return validateUserTeamsImportData(data.Teams)
	} else {
		return nil
	}
}

func validateUserTeamsImportData(data *[]UserTeamImportData) *model.AppError {
	if data == nil {
		return nil
	}

	for _, tdata := range *data {
		if tdata.Name == nil {
			return model.NewAppError("BulkImport", "app.import.validate_user_teams_import_data.team_name_missing.error", nil, "", http.StatusBadRequest)
		}

		if tdata.Roles != nil && !model.IsValidUserRoles(*tdata.Roles) {
			return model.NewAppError("BulkImport", "app.import.validate_user_teams_import_data.invalid_roles.error", nil, "", http.StatusBadRequest)
		}

		if tdata.Channels != nil {
			if err := validateUserChannelsImportData(tdata.Channels); err != nil {
				return err
			}
		}
	}

	return nil
}

func validateUserChannelsImportData(data *[]UserChannelImportData) *model.AppError {
	if data == nil {
		return nil
	}

	for _, cdata := range *data {
		if cdata.Name == nil {
			return model.NewAppError("BulkImport", "app.import.validate_user_channels_import_data.channel_name_missing.error", nil, "", http.StatusBadRequest)
		}

		if cdata.Roles != nil && !model.IsValidUserRoles(*cdata.Roles) {
			return model.NewAppError("BulkImport", "app.import.validate_user_channels_import_data.invalid_roles.error", nil, "", http.StatusBadRequest)
		}

		if cdata.NotifyProps != nil {
			if cdata.NotifyProps.Desktop != nil && !model.IsChannelNotifyLevelValid(*cdata.NotifyProps.Desktop) {
				return model.NewAppError("BulkImport", "app.import.validate_user_channels_import_data.invalid_notify_props_desktop.error", nil, "", http.StatusBadRequest)
			}

			if cdata.NotifyProps.Mobile != nil && !model.IsChannelNotifyLevelValid(*cdata.NotifyProps.Mobile) {
				return model.NewAppError("BulkImport", "app.import.validate_user_channels_import_data.invalid_notify_props_mobile.error", nil, "", http.StatusBadRequest)
			}

			if cdata.NotifyProps.MarkUnread != nil && !model.IsChannelMarkUnreadLevelValid(*cdata.NotifyProps.MarkUnread) {
				return model.NewAppError("BulkImport", "app.import.validate_user_channels_import_data.invalid_notify_props_mark_unread.error", nil, "", http.StatusBadRequest)
			}
		}
	}

	return nil
}

func (a *App) ImportReaction(data *ReactionImportData, post *model.Post, dryRun bool) *model.AppError {
	if err := validateReactionImportData(data, post.CreateAt); err != nil {
		return err
	}

	var user *model.User
	if result := <-a.Srv.Store.User().GetByUsername(*data.User); result.Err != nil {
		return model.NewAppError("BulkImport", "app.import.import_post.user_not_found.error", map[string]interface{}{"Username": data.User}, result.Err.Error(), http.StatusBadRequest)
	} else {
		user = result.Data.(*model.User)
	}
	reaction := &model.Reaction{
		UserId:    user.Id,
		PostId:    post.Id,
		EmojiName: *data.EmojiName,
		CreateAt:  *data.CreateAt,
	}
	if result := <-a.Srv.Store.Reaction().Save(reaction); result.Err != nil {
		return result.Err
	}
	return nil
}

func (a *App) ImportReply(data *ReplyImportData, post *model.Post, dryRun bool) *model.AppError {
	if err := validateReplyImportData(data, post.CreateAt, a.MaxPostSize()); err != nil {
		return err
	}

	var user *model.User
	if result := <-a.Srv.Store.User().GetByUsername(*data.User); result.Err != nil {
		return model.NewAppError("BulkImport", "app.import.import_post.user_not_found.error", map[string]interface{}{"Username": data.User}, result.Err.Error(), http.StatusBadRequest)
	} else {
		user = result.Data.(*model.User)
	}

	// Check if this post already exists.
	var replies []*model.Post
	if result := <-a.Srv.Store.Post().GetPostsCreatedAt(post.ChannelId, *data.CreateAt); result.Err != nil {
		return result.Err
	} else {
		replies = result.Data.([]*model.Post)
	}

	var reply *model.Post
	for _, r := range replies {
		if r.Message == *data.Message {
			reply = r
			break
		}
	}

	if reply == nil {
		reply = &model.Post{}
	}
	reply.UserId = user.Id
	reply.ChannelId = post.ChannelId
	reply.ParentId = post.Id
	reply.RootId = post.Id
	reply.Message = *data.Message
	reply.CreateAt = *data.CreateAt

	if reply.Id == "" {
		if result := <-a.Srv.Store.Post().Save(reply); result.Err != nil {
			return result.Err
		}
	} else {
		if result := <-a.Srv.Store.Post().Overwrite(reply); result.Err != nil {
			return result.Err
		}
	}
	return nil
}

func (a *App) ImportPost(data *PostImportData, dryRun bool) *model.AppError {
	if err := validatePostImportData(data, a.MaxPostSize()); err != nil {
		return err
	}

	// If this is a Dry Run, do not continue any further.
	if dryRun {
		return nil
	}

	var team *model.Team
	if result := <-a.Srv.Store.Team().GetByName(*data.Team); result.Err != nil {
		return model.NewAppError("BulkImport", "app.import.import_post.team_not_found.error", map[string]interface{}{"TeamName": *data.Team}, result.Err.Error(), http.StatusBadRequest)
	} else {
		team = result.Data.(*model.Team)
	}

	var channel *model.Channel
	if result := <-a.Srv.Store.Channel().GetByName(team.Id, *data.Channel, false); result.Err != nil {
		return model.NewAppError("BulkImport", "app.import.import_post.channel_not_found.error", map[string]interface{}{"ChannelName": *data.Channel}, result.Err.Error(), http.StatusBadRequest)
	} else {
		channel = result.Data.(*model.Channel)
	}

	var user *model.User
	if result := <-a.Srv.Store.User().GetByUsername(*data.User); result.Err != nil {
		return model.NewAppError("BulkImport", "app.import.import_post.user_not_found.error", map[string]interface{}{"Username": *data.User}, result.Err.Error(), http.StatusBadRequest)
	} else {
		user = result.Data.(*model.User)
	}

	// Check if this post already exists.
	var posts []*model.Post
	if result := <-a.Srv.Store.Post().GetPostsCreatedAt(channel.Id, *data.CreateAt); result.Err != nil {
		return result.Err
	} else {
		posts = result.Data.([]*model.Post)
	}

	var post *model.Post
	for _, p := range posts {
		if p.Message == *data.Message {
			post = p
			break
		}
	}

	if post == nil {
		post = &model.Post{}
	}

	post.ChannelId = channel.Id
	post.Message = *data.Message
	post.UserId = user.Id
	post.CreateAt = *data.CreateAt

	post.Hashtags, _ = model.ParseHashtags(post.Message)

	if post.Id == "" {
		if result := <-a.Srv.Store.Post().Save(post); result.Err != nil {
			return result.Err
		}
	} else {
		if result := <-a.Srv.Store.Post().Overwrite(post); result.Err != nil {
			return result.Err
		}
	}

	if data.FlaggedBy != nil {
		var preferences model.Preferences

		for _, username := range *data.FlaggedBy {
			var user *model.User

			if result := <-a.Srv.Store.User().GetByUsername(username); result.Err != nil {
				return model.NewAppError("BulkImport", "app.import.import_post.user_not_found.error", map[string]interface{}{"Username": username}, result.Err.Error(), http.StatusBadRequest)
			} else {
				user = result.Data.(*model.User)
			}

			preferences = append(preferences, model.Preference{
				UserId:   user.Id,
				Category: model.PREFERENCE_CATEGORY_FLAGGED_POST,
				Name:     post.Id,
				Value:    "true",
			})
		}

		if len(preferences) > 0 {
			if result := <-a.Srv.Store.Preference().Save(&preferences); result.Err != nil {
				return model.NewAppError("BulkImport", "app.import.import_post.save_preferences.error", nil, result.Err.Error(), http.StatusInternalServerError)
			}
		}
	}

	if data.Reactions != nil {
		for _, reaction := range *data.Reactions {
			if err := a.ImportReaction(&reaction, post, dryRun); err != nil {
				return err
			}
		}
	}

	if data.Replies != nil {
		for _, reply := range *data.Replies {
			if err := a.ImportReply(&reply, post, dryRun); err != nil {
				return err
			}
		}
	}

	return nil
}

func validateReactionImportData(data *ReactionImportData, parentCreateAt int64) *model.AppError {
	if data.User == nil {
		return model.NewAppError("BulkImport", "app.import.validate_reaction_import_data.user_missing.error", nil, "", http.StatusBadRequest)
	}

	if data.EmojiName == nil {
		return model.NewAppError("BulkImport", "app.import.validate_reaction_import_data.emoji_name_missing.error", nil, "", http.StatusBadRequest)
	} else if utf8.RuneCountInString(*data.EmojiName) > model.EMOJI_NAME_MAX_LENGTH {
		return model.NewAppError("BulkImport", "app.import.validate_reaction_import_data.emoji_name_length.error", nil, "", http.StatusBadRequest)
	}

	if data.CreateAt == nil {
		return model.NewAppError("BulkImport", "app.import.validate_reaction_import_data.create_at_missing.error", nil, "", http.StatusBadRequest)
	} else if *data.CreateAt == 0 {
		return model.NewAppError("BulkImport", "app.import.validate_reaction_import_data.create_at_zero.error", nil, "", http.StatusBadRequest)
	} else if *data.CreateAt < parentCreateAt {
		return model.NewAppError("BulkImport", "app.import.validate_reaction_import_data.create_at_before_parent.error", nil, "", http.StatusBadRequest)
	}

	return nil
}

func validateReplyImportData(data *ReplyImportData, parentCreateAt int64, maxPostSize int) *model.AppError {
	if data.User == nil {
		return model.NewAppError("BulkImport", "app.import.validate_reply_import_data.user_missing.error", nil, "", http.StatusBadRequest)
	}

	if data.Message == nil {
		return model.NewAppError("BulkImport", "app.import.validate_reply_import_data.message_missing.error", nil, "", http.StatusBadRequest)
	} else if utf8.RuneCountInString(*data.Message) > maxPostSize {
		return model.NewAppError("BulkImport", "app.import.validate_reply_import_data.message_length.error", nil, "", http.StatusBadRequest)
	}

	if data.CreateAt == nil {
		return model.NewAppError("BulkImport", "app.import.validate_reply_import_data.create_at_missing.error", nil, "", http.StatusBadRequest)
	} else if *data.CreateAt == 0 {
		return model.NewAppError("BulkImport", "app.import.validate_reply_import_data.create_at_zero.error", nil, "", http.StatusBadRequest)
	} else if *data.CreateAt < parentCreateAt {
		return model.NewAppError("BulkImport", "app.import.validate_reply_import_data.create_at_before_parent.error", nil, "", http.StatusBadRequest)
	}

	return nil
}

func validatePostImportData(data *PostImportData, maxPostSize int) *model.AppError {
	if data.Team == nil {
		return model.NewAppError("BulkImport", "app.import.validate_post_import_data.team_missing.error", nil, "", http.StatusBadRequest)
	}

	if data.Channel == nil {
		return model.NewAppError("BulkImport", "app.import.validate_post_import_data.channel_missing.error", nil, "", http.StatusBadRequest)
	}

	if data.User == nil {
		return model.NewAppError("BulkImport", "app.import.validate_post_import_data.user_missing.error", nil, "", http.StatusBadRequest)
	}

	if data.Message == nil {
		return model.NewAppError("BulkImport", "app.import.validate_post_import_data.message_missing.error", nil, "", http.StatusBadRequest)
	} else if utf8.RuneCountInString(*data.Message) > maxPostSize {
		return model.NewAppError("BulkImport", "app.import.validate_post_import_data.message_length.error", nil, "", http.StatusBadRequest)
	}

	if data.CreateAt == nil {
		return model.NewAppError("BulkImport", "app.import.validate_post_import_data.create_at_missing.error", nil, "", http.StatusBadRequest)
	} else if *data.CreateAt == 0 {
		return model.NewAppError("BulkImport", "app.import.validate_post_import_data.create_at_zero.error", nil, "", http.StatusBadRequest)
	}

	if data.Reactions != nil {
		for _, reaction := range *data.Reactions {
			validateReactionImportData(&reaction, *data.CreateAt)
		}
	}

	if data.Replies != nil {
		for _, reply := range *data.Replies {
			validateReplyImportData(&reply, *data.CreateAt, maxPostSize)
		}
	}

	return nil
}

func (a *App) ImportDirectChannel(data *DirectChannelImportData, dryRun bool) *model.AppError {
	if err := validateDirectChannelImportData(data); err != nil {
		return err
	}

	// If this is a Dry Run, do not continue any further.
	if dryRun {
		return nil
	}

	var userIds []string
	userMap := make(map[string]string)
	for _, username := range *data.Members {
		if result := <-a.Srv.Store.User().GetByUsername(username); result.Err == nil {
			user := result.Data.(*model.User)
			userIds = append(userIds, user.Id)
			userMap[username] = user.Id
		} else {
			return model.NewAppError("BulkImport", "app.import.import_direct_channel.member_not_found.error", nil, result.Err.Error(), http.StatusBadRequest)
		}
	}

	var channel *model.Channel

	if len(userIds) == 2 {
		ch, err := a.createDirectChannel(userIds[0], userIds[1])
		if err != nil && err.Id != store.CHANNEL_EXISTS_ERROR {
			return model.NewAppError("BulkImport", "app.import.import_direct_channel.create_direct_channel.error", nil, err.Error(), http.StatusBadRequest)
		} else {
			channel = ch
		}
	} else {
		ch, err := a.createGroupChannel(userIds, userIds[0])
		if err != nil && err.Id != store.CHANNEL_EXISTS_ERROR {
			return model.NewAppError("BulkImport", "app.import.import_direct_channel.create_group_channel.error", nil, err.Error(), http.StatusBadRequest)
		} else {
			channel = ch
		}
	}

	var preferences model.Preferences

	for _, userId := range userIds {
		preferences = append(preferences, model.Preference{
			UserId:   userId,
			Category: model.PREFERENCE_CATEGORY_DIRECT_CHANNEL_SHOW,
			Name:     channel.Id,
			Value:    "true",
		})
	}

	if data.FavoritedBy != nil {
		for _, favoriter := range *data.FavoritedBy {
			preferences = append(preferences, model.Preference{
				UserId:   userMap[favoriter],
				Category: model.PREFERENCE_CATEGORY_FAVORITE_CHANNEL,
				Name:     channel.Id,
				Value:    "true",
			})
		}
	}

	if result := <-a.Srv.Store.Preference().Save(&preferences); result.Err != nil {
		result.Err.StatusCode = http.StatusBadRequest
		return result.Err
	}

	if data.Header != nil {
		channel.Header = *data.Header
		if result := <-a.Srv.Store.Channel().Update(channel); result.Err != nil {
			return model.NewAppError("BulkImport", "app.import.import_direct_channel.update_header_failed.error", nil, result.Err.Error(), http.StatusBadRequest)
		}
	}

	return nil
}

func validateDirectChannelImportData(data *DirectChannelImportData) *model.AppError {
	if data.Members == nil {
		return model.NewAppError("BulkImport", "app.import.validate_direct_channel_import_data.members_required.error", nil, "", http.StatusBadRequest)
	}

	if len(*data.Members) != 2 {
		if len(*data.Members) < model.CHANNEL_GROUP_MIN_USERS {
			return model.NewAppError("BulkImport", "app.import.validate_direct_channel_import_data.members_too_few.error", nil, "", http.StatusBadRequest)
		} else if len(*data.Members) > model.CHANNEL_GROUP_MAX_USERS {
			return model.NewAppError("BulkImport", "app.import.validate_direct_channel_import_data.members_too_many.error", nil, "", http.StatusBadRequest)
		}
	}

	if data.Header != nil && utf8.RuneCountInString(*data.Header) > model.CHANNEL_HEADER_MAX_RUNES {
		return model.NewAppError("BulkImport", "app.import.validate_direct_channel_import_data.header_length.error", nil, "", http.StatusBadRequest)
	}

	if data.FavoritedBy != nil {
		for _, favoriter := range *data.FavoritedBy {
			found := false
			for _, member := range *data.Members {
				if favoriter == member {
					found = true
					break
				}
			}
			if !found {
				return model.NewAppError("BulkImport", "app.import.validate_direct_channel_import_data.unknown_favoriter.error", map[string]interface{}{"Username": favoriter}, "", http.StatusBadRequest)
			}
		}
	}

	return nil
}

func (a *App) ImportDirectPost(data *DirectPostImportData, dryRun bool) *model.AppError {
	if err := validateDirectPostImportData(data, a.MaxPostSize()); err != nil {
		return err
	}

	// If this is a Dry Run, do not continue any further.
	if dryRun {
		return nil
	}

	var userIds []string
	for _, username := range *data.ChannelMembers {
		if result := <-a.Srv.Store.User().GetByUsername(username); result.Err == nil {
			user := result.Data.(*model.User)
			userIds = append(userIds, user.Id)
		} else {
			return model.NewAppError("BulkImport", "app.import.import_direct_post.channel_member_not_found.error", nil, result.Err.Error(), http.StatusBadRequest)
		}
	}

	var channel *model.Channel
	if len(userIds) == 2 {
		ch, err := a.createDirectChannel(userIds[0], userIds[1])
		if err != nil && err.Id != store.CHANNEL_EXISTS_ERROR {
			return model.NewAppError("BulkImport", "app.import.import_direct_post.create_direct_channel.error", nil, err.Error(), http.StatusBadRequest)
		} else {
			channel = ch
		}
	} else {
		ch, err := a.createGroupChannel(userIds, userIds[0])
		if err != nil && err.Id != store.CHANNEL_EXISTS_ERROR {
			return model.NewAppError("BulkImport", "app.import.import_direct_post.create_group_channel.error", nil, err.Error(), http.StatusBadRequest)
		} else {
			channel = ch
		}
	}

	var user *model.User
	if result := <-a.Srv.Store.User().GetByUsername(*data.User); result.Err != nil {
		return model.NewAppError("BulkImport", "app.import.import_direct_post.user_not_found.error", map[string]interface{}{"Username": *data.User}, "", http.StatusBadRequest)
	} else {
		user = result.Data.(*model.User)
	}

	// Check if this post already exists.
	var posts []*model.Post
	if result := <-a.Srv.Store.Post().GetPostsCreatedAt(channel.Id, *data.CreateAt); result.Err != nil {
		return result.Err
	} else {
		posts = result.Data.([]*model.Post)
	}

	var post *model.Post
	for _, p := range posts {
		if p.Message == *data.Message {
			post = p
			break
		}
	}

	if post == nil {
		post = &model.Post{}
	}

	post.ChannelId = channel.Id
	post.Message = *data.Message
	post.UserId = user.Id
	post.CreateAt = *data.CreateAt

	post.Hashtags, _ = model.ParseHashtags(post.Message)

	if post.Id == "" {
		if result := <-a.Srv.Store.Post().Save(post); result.Err != nil {
			return result.Err
		}
	} else {
		if result := <-a.Srv.Store.Post().Overwrite(post); result.Err != nil {
			return result.Err
		}
	}

	if data.FlaggedBy != nil {
		var preferences model.Preferences

		for _, username := range *data.FlaggedBy {
			var user *model.User

			if result := <-a.Srv.Store.User().GetByUsername(username); result.Err != nil {
				return model.NewAppError("BulkImport", "app.import.import_direct_post.user_not_found.error", map[string]interface{}{"Username": username}, "", http.StatusBadRequest)
			} else {
				user = result.Data.(*model.User)
			}

			preferences = append(preferences, model.Preference{
				UserId:   user.Id,
				Category: model.PREFERENCE_CATEGORY_FLAGGED_POST,
				Name:     post.Id,
				Value:    "true",
			})
		}

		if len(preferences) > 0 {
			if result := <-a.Srv.Store.Preference().Save(&preferences); result.Err != nil {
				return model.NewAppError("BulkImport", "app.import.import_direct_post.save_preferences.error", nil, result.Err.Error(), http.StatusInternalServerError)
			}
		}
	}

	if data.Reactions != nil {
		for _, reaction := range *data.Reactions {
			if err := a.ImportReaction(&reaction, post, dryRun); err != nil {
				return err
			}
		}
	}

	if data.Replies != nil {
		for _, reply := range *data.Replies {
			if err := a.ImportReply(&reply, post, dryRun); err != nil {
				return err
			}
		}
	}

	return nil
}

func validateDirectPostImportData(data *DirectPostImportData, maxPostSize int) *model.AppError {
	if data.ChannelMembers == nil {
		return model.NewAppError("BulkImport", "app.import.validate_direct_post_import_data.channel_members_required.error", nil, "", http.StatusBadRequest)
	}

	if len(*data.ChannelMembers) != 2 {
		if len(*data.ChannelMembers) < model.CHANNEL_GROUP_MIN_USERS {
			return model.NewAppError("BulkImport", "app.import.validate_direct_post_import_data.channel_members_too_few.error", nil, "", http.StatusBadRequest)
		} else if len(*data.ChannelMembers) > model.CHANNEL_GROUP_MAX_USERS {
			return model.NewAppError("BulkImport", "app.import.validate_direct_post_import_data.channel_members_too_many.error", nil, "", http.StatusBadRequest)
		}
	}

	if data.User == nil {
		return model.NewAppError("BulkImport", "app.import.validate_direct_post_import_data.user_missing.error", nil, "", http.StatusBadRequest)
	}

	if data.Message == nil {
		return model.NewAppError("BulkImport", "app.import.validate_direct_post_import_data.message_missing.error", nil, "", http.StatusBadRequest)
	} else if utf8.RuneCountInString(*data.Message) > maxPostSize {
		return model.NewAppError("BulkImport", "app.import.validate_direct_post_import_data.message_length.error", nil, "", http.StatusBadRequest)
	}

	if data.CreateAt == nil {
		return model.NewAppError("BulkImport", "app.import.validate_direct_post_import_data.create_at_missing.error", nil, "", http.StatusBadRequest)
	} else if *data.CreateAt == 0 {
		return model.NewAppError("BulkImport", "app.import.validate_direct_post_import_data.create_at_zero.error", nil, "", http.StatusBadRequest)
	}

	if data.FlaggedBy != nil {
		for _, flagger := range *data.FlaggedBy {
			found := false
			for _, member := range *data.ChannelMembers {
				if flagger == member {
					found = true
					break
				}
			}
			if !found {
				return model.NewAppError("BulkImport", "app.import.validate_direct_post_import_data.unknown_flagger.error", map[string]interface{}{"Username": flagger}, "", http.StatusBadRequest)
			}
		}
	}

	if data.Reactions != nil {
		for _, reaction := range *data.Reactions {
			validateReactionImportData(&reaction, *data.CreateAt)
		}
	}

	if data.Replies != nil {
		for _, reply := range *data.Replies {
			validateReplyImportData(&reply, *data.CreateAt, maxPostSize)
		}
	}

	return nil
}

func (a *App) ImportEmoji(data *EmojiImportData, dryRun bool) *model.AppError {
	if err := validateEmojiImportData(data); err != nil {
		return err
	}

	// If this is a Dry Run, do not continue any further.
	if dryRun {
		return nil
	}

	var emoji *model.Emoji
	var err *model.AppError

	emoji, err = a.GetEmojiByName(*data.Name)
	if err != nil && err.StatusCode != http.StatusNotFound {
		return err
	}

	alreadyExists := emoji != nil

	if !alreadyExists {
		emoji = &model.Emoji{
			Name: *data.Name,
		}
		emoji.PreSave()
	}

	file, fileErr := os.Open(*data.Image)
	if fileErr != nil {
		return model.NewAppError("BulkImport", "app.import.emoji.bad_file.error", map[string]interface{}{"EmojiName": *data.Name}, "", http.StatusBadRequest)
	}

	if _, err := a.WriteFile(file, getEmojiImagePath(emoji.Id)); err != nil {
		return err
	}

	if !alreadyExists {
		if result := <-a.Srv.Store.Emoji().Save(emoji); result.Err != nil {
			return result.Err
		}
	}

	return nil
}

func validateEmojiImportData(data *EmojiImportData) *model.AppError {
	if data == nil {
		return model.NewAppError("BulkImport", "app.import.validate_emoji_import_data.empty.error", nil, "", http.StatusBadRequest)
	}

	if data.Name == nil || len(*data.Name) == 0 {
		return model.NewAppError("BulkImport", "app.import.validate_emoji_import_data.name_missing.error", nil, "", http.StatusBadRequest)
	}

	if err := model.IsValidEmojiName(*data.Name); err != nil {
		return err
	}

	if data.Image == nil || len(*data.Image) == 0 {
		return model.NewAppError("BulkImport", "app.import.validate_emoji_import_data.image_missing.error", nil, "", http.StatusBadRequest)
	}

	return nil
}

//
// -- Old SlackImport Functions --
// Import functions are sutible for entering posts and users into the database without
// some of the usual checks. (IsValid is still run)
//

func (a *App) OldImportPost(post *model.Post) {
	// Workaround for empty messages, which may be the case if they are webhook posts.
	firstIteration := true
	maxPostSize := a.MaxPostSize()
	for messageRuneCount := utf8.RuneCountInString(post.Message); messageRuneCount > 0 || firstIteration; messageRuneCount = utf8.RuneCountInString(post.Message) {
		firstIteration = false
		var remainder string
		if messageRuneCount > maxPostSize {
			remainder = string(([]rune(post.Message))[maxPostSize:])
			post.Message = truncateRunes(post.Message, maxPostSize)
		} else {
			remainder = ""
		}

		post.Hashtags, _ = model.ParseHashtags(post.Message)

		if result := <-a.Srv.Store.Post().Save(post); result.Err != nil {
			mlog.Debug(fmt.Sprintf("Error saving post. user=%v, message=%v", post.UserId, post.Message))
		}

		for _, fileId := range post.FileIds {
			if result := <-a.Srv.Store.FileInfo().AttachToPost(fileId, post.Id); result.Err != nil {
				mlog.Error(fmt.Sprintf("Error attaching files to post. postId=%v, fileIds=%v, message=%v", post.Id, post.FileIds, result.Err), mlog.String("post_id", post.Id))
			}
		}

		post.Id = ""
		post.CreateAt++
		post.Message = remainder
	}
}

func (a *App) OldImportUser(team *model.Team, user *model.User) *model.User {
	user.MakeNonNil()

	user.Roles = model.SYSTEM_USER_ROLE_ID

	if result := <-a.Srv.Store.User().Save(user); result.Err != nil {
		mlog.Error(fmt.Sprintf("Error saving user. err=%v", result.Err))
		return nil
	} else {
		ruser := result.Data.(*model.User)

		if cresult := <-a.Srv.Store.User().VerifyEmail(ruser.Id); cresult.Err != nil {
			mlog.Error(fmt.Sprintf("Failed to set email verified err=%v", cresult.Err))
		}

		if err := a.JoinUserToTeam(team, user, ""); err != nil {
			mlog.Error(fmt.Sprintf("Failed to join team when importing err=%v", err))
		}

		return ruser
	}
}

func (a *App) OldImportChannel(channel *model.Channel) *model.Channel {
	if result := <-a.Srv.Store.Channel().Save(channel, *a.Config().TeamSettings.MaxChannelsPerTeam); result.Err != nil {
		return nil
	} else {
		sc := result.Data.(*model.Channel)

		return sc
	}
}

func (a *App) OldImportFile(timestamp time.Time, file io.Reader, teamId string, channelId string, userId string, fileName string) (*model.FileInfo, error) {
	buf := bytes.NewBuffer(nil)
	io.Copy(buf, file)
	data := buf.Bytes()

	fileInfo, err := a.DoUploadFile(timestamp, teamId, channelId, userId, fileName, data)
	if err != nil {
		return nil, err
	}

	if fileInfo.IsImage() && fileInfo.MimeType != "image/svg+xml" {
		img, width, height := prepareImage(data)
		if img != nil {
			a.generateThumbnailImage(*img, fileInfo.ThumbnailPath, width, height)
			a.generatePreviewImage(*img, fileInfo.PreviewPath, width)
		}
	}

	return fileInfo, nil
}

func (a *App) OldImportIncomingWebhookPost(post *model.Post, props model.StringInterface) {
	linkWithTextRegex := regexp.MustCompile(`<([^<\|]+)\|([^>]+)>`)
	post.Message = linkWithTextRegex.ReplaceAllString(post.Message, "[${2}](${1})")

	post.AddProp("from_webhook", "true")

	if _, ok := props["override_username"]; !ok {
		post.AddProp("override_username", model.DEFAULT_WEBHOOK_USERNAME)
	}

	if len(props) > 0 {
		for key, val := range props {
			if key == "attachments" {
				if attachments, success := val.([]*model.SlackAttachment); success {
					parseSlackAttachment(post, attachments)
				}
			} else if key != "from_webhook" {
				post.AddProp(key, val)
			}
		}
	}

	a.OldImportPost(post)
}
